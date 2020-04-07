package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	_log "log"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/bmatcuk/doublestar"
	"github.com/logrusorgru/aurora"
	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
)

type tConfig struct {
	ACL          string
	Bucket       string
	CacheControl string
	Clean        bool
	Debug        bool
	Deploy       bool
	DryRun       bool
	Force        bool
	Keep         int
	Pattern      string
	Quiet        bool
	Release      int
	Source       string
	Yes          bool
}

type tFile struct {
	Path            string
	ContentType     *string
	ContentEncoding *string
}

type tObject struct {
	CacheControl    *string
	ContentType     *string
	ContentEncoding *string
	Metadata        map[string]*string
}

func init() {
	_log.SetOutput(ioutil.Discard)

	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)
}

func main() {
	var config tConfig
	defaultRelease := 0

	if env, found := os.LookupEnv("BUILD_NUMBER"); found {
		if i, e := strconv.Atoi(env); e == nil {
			defaultRelease = i
		}
	}

	flag.BoolVar(&config.Clean, "clean", true, "Remove old assets")
	flag.BoolVar(&config.Debug, "debug", false, "Debug logging")
	flag.BoolVar(&config.Deploy, "deploy", true, "Deploy new assets")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Dry-run")
	flag.BoolVar(&config.Force, "force", false, "")
	flag.BoolVar(&config.Quiet, "quiet", false, "Do not say what I'm doing")
	flag.BoolVar(&config.Yes, "yes", false, "Do not ask for confirmation")
	flag.IntVar(&config.Keep, "keep", 10, "Number of releases to keep")
	flag.IntVar(&config.Release, "release", defaultRelease, "Release number")
	flag.StringVar(&config.ACL, "acl", "public-read", "File ACL")
	flag.StringVar(&config.Bucket, "bucket", "", "S3 Bucket")
	flag.StringVar(&config.CacheControl, "cache-control", "public,immutable,max-age=31536000", "")
	flag.StringVar(&config.Pattern, "pattern", "**/*", "Assets to deploy")
	flag.StringVar(&config.Source, "source", ".", "Source directory")
	flag.Parse()

	if config.Quiet {
		log.SetLevel(log.WarnLevel)
	}

	if config.Debug {
		log.SetLevel(log.DebugLevel)
	}

	if config.Release < 1 {
		log.Fatalln("ERROR: Release number is required and must be > 0.")
	}

	if len(config.Bucket) < 1 {
		log.Fatalln("Bucket name required")
	}

	log.Printf("Release: %d\n", config.Release)

	source, err := homedir.Expand(config.Source)
	if err != nil {
		log.Fatalf("Error expanding source: %s", err)
	}

	localFiles := make(map[string]tFile)

	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		matched, err := doublestar.Match(config.Pattern, path)
		if err != nil {
			return err
		}

		if !matched {
			// Skipping non-matched file
			return nil
		}

		rel, err := filepath.Rel(config.Source, path)
		if err != nil {
			log.Debugf("Skipping, filepath.Rel(%s) error: %s", path, err)
			return nil
		}

		file := tFile{Path: path}

		ext := filepath.Ext(path)
		switch ext {
		case ".gz":
			file.ContentEncoding = aws.String("gzip")
			ext = filepath.Ext(path[0 : len(path)-3])
		case ".br":
			file.ContentEncoding = aws.String("br")
			ext = filepath.Ext(path[0 : len(path)-3])
		case ".zz":
			file.ContentEncoding = aws.String("deflate")
			ext = filepath.Ext(path[0 : len(path)-3])
		}

		mime := mime.TypeByExtension(ext)
		if len(mime) > 0 {
			file.ContentType = aws.String(mime)
		}

		localFiles[rel] = file
		return nil
	})

	if err != nil {
		log.Fatal(err)
	}

	log.Infof("Found %d files", len(localFiles))
	log.Infof("Reading bucket for existing files...")

	s3Config := &aws.Config{
		Endpoint:         aws.String("https://s3.xopic.de"),
		Region:           aws.String("default"),
		DisableSSL:       aws.Bool(true),
		S3ForcePathStyle: aws.Bool(true),
	}

	s3s := session.New(s3Config)
	s3c := s3.New(s3s)

	remoteFiles := make(map[string]tObject)

	listOpts := s3.ListObjectsV2Input{Bucket: &config.Bucket}
	err = s3c.ListObjectsV2Pages(&listOpts, func(page *s3.ListObjectsV2Output, last bool) bool {
		for _, item := range page.Contents {
			obj, err := s3c.HeadObject(&s3.HeadObjectInput{Bucket: &config.Bucket, Key: item.Key})
			if err != nil {
				log.Debugf("Skip %s: %v", *item.Key, err)
			}

			remoteFiles[*item.Key] = tObject{
				CacheControl:    obj.CacheControl,
				ContentEncoding: obj.ContentEncoding,
				ContentType:     obj.ContentType,
				Metadata:        obj.Metadata,
			}
		}

		return true
	})

	if err != nil {
		log.Fatalln(err)
	}

	var allFiles []string

	newFiles := make(map[string]tFile)
	differentFiles := make(map[string]tObject)
	for key, file := range localFiles {
		allFiles = append(allFiles, key)

		if config.Force {
			newFiles[key] = file
			continue
		}

		obj, remoteExists := remoteFiles[key]

		if !remoteExists && (config.Deploy || config.DryRun) {
			newFiles[key] = file
			continue
		}

		update := false

		if obj.CacheControl != nil {
			if *obj.CacheControl != config.CacheControl {
				log.Warnf("%s: Wrong Cache-Control, expected: %s, got: %s", key, config.CacheControl, *obj.CacheControl)
				obj.CacheControl = &config.CacheControl
				update = true
			}
		} else {
			log.Warnf("%s: Missing Cache-Control", key)
			obj.CacheControl = &config.CacheControl
			update = true
		}

		if obj.ContentType != nil {
			if file.ContentType != nil && *file.ContentType != *obj.ContentType {
				log.Warnf("%s: Wrong Content-Type, expected %s, got %s", key, *file.ContentType, *obj.ContentType)
				obj.ContentType = file.ContentType
				update = true
			}
		} else if file.ContentType != nil {
			log.Warnf("%s: Missing Content-Type: %s", key, *file.ContentType)
			obj.ContentType = file.ContentType
			update = true
		}

		if obj.ContentEncoding != nil {
			if file.ContentEncoding != nil && *file.ContentEncoding != *obj.ContentEncoding {
				log.Warnf("%s: Wrong Content-Encoding, expected %s, got %s", key, *file.ContentEncoding, *obj.ContentEncoding)
				obj.ContentEncoding = file.ContentEncoding
				update = true
			}
		} else if file.ContentEncoding != nil {
			log.Warnf("%s: Missing Content-Encoding: %s", key, *file.ContentEncoding)
			obj.ContentEncoding = file.ContentEncoding
			update = true
		}

		releaseString := aws.String(strconv.Itoa(config.Release))

		if obj.Metadata == nil {
			log.Warnf("%s: Missing object metadata", key)
			obj.Metadata = map[string]*string{"Release": releaseString}
			update = true
		} else {
			val, ok := obj.Metadata["Release"]

			if !ok || val == nil {
				log.Warnf("%s: Missing release metadata", key)
				obj.Metadata = map[string]*string{"Release": releaseString}
				update = true
			} else if *val != *releaseString {
				i, _ := strconv.Atoi(*val)
				if keep(&config, i) {
					log.Debugf("%s: Update release from %s to %s", key, *val, *releaseString)
					update = true
				}
			}
		}

		if update {
			differentFiles[key] = obj
		} else {
			log.Debugf("%s: up-to-date", key)
		}
	}

	removeFiles := make(map[string]bool)
	keepFiles := make(map[string]bool)
	for file, obj := range remoteFiles {
		if _, ok := localFiles[file]; !ok {
			allFiles = append(allFiles, file)

			release := 0
			if obj.Metadata != nil {
				val, _ := obj.Metadata["Release"]
				if val != nil {
					release, _ = strconv.Atoi(*val)
				}
			}

			if (config.Clean || config.DryRun) && !keep(&config, release) {
				removeFiles[file] = true
			} else {
				keepFiles[file] = true
			}
		}
	}

	sort.Strings(allFiles)

	if !(config.Quiet && config.Yes) {
		fmt.Println()

		if len(newFiles) > 0 || len(differentFiles) > 0 || len(removeFiles) > 0 {
			fmt.Printf("An execution plan has been generated and is shown below.\n")
			fmt.Printf("Actions are indicated with the following symbols:\n")
			fmt.Printf("  %s Upload new file\n", aurora.Green("+"))
			fmt.Printf("  %s Update remote file in-place\n", aurora.Yellow("~"))
			fmt.Printf("  %s Delete remote file\n", aurora.Red("-"))
			fmt.Println()
			fmt.Printf("Current execution plan:\n")

			for _, file := range allFiles {
				if _, ok := newFiles[file]; ok {
					fmt.Printf("  %s %s\n", aurora.Green("+"), file)
				}
				if _, ok := differentFiles[file]; ok {
					fmt.Printf("  %s %s\n", aurora.Yellow("~"), file)
				}
				if _, ok := removeFiles[file]; ok {
					fmt.Printf("  %s %s\n", aurora.Red("-"), file)
				}
				if _, ok := keepFiles[file]; ok {
					fmt.Printf("    %s\n", file)
				}
			}
			fmt.Println()

		} else {
			fmt.Println("No changes detected. All files up-to-date.")
			fmt.Println()
			os.Exit(0)
		}
	}

	if config.DryRun {
		os.Exit(0)
	}

	if !config.Yes && !confirm("Execute?") {
		fmt.Println("Abort.")
		os.Exit(0)
	}

	if len(newFiles) > 0 {
		log.Infoln("Uploading new files...")

		uploader := s3manager.NewUploader(s3s)
		for key, file := range newFiles {
			log.Debugf("Uploading %s...", key)

			fd, err := os.Open(file.Path)
			if err != nil {
				log.Errorf("Upload failed: %v", err)
			}
			defer fd.Close()

			opts := s3manager.UploadInput{
				Bucket:          &config.Bucket,
				Key:             &key,
				Body:            fd,
				ACL:             &config.ACL,
				CacheControl:    &config.CacheControl,
				ContentType:     file.ContentType,
				ContentEncoding: file.ContentEncoding,
				Metadata:        map[string]*string{"Release": aws.String(strconv.Itoa(config.Release))},
			}

			_, err = uploader.Upload(&opts)
			if err != nil {
				log.Errorf("Upload failed: %v", err)
			}
		}
	}

	if len(differentFiles) > 0 {
		for key, obj := range differentFiles {
			log.Debugf("Updating in-place: %s", key)

			meta := obj.Metadata
			if meta != nil {
				meta["Release"] = aws.String(strconv.Itoa(config.Release))
			} else {
				meta = map[string]*string{"Release": aws.String(strconv.Itoa(config.Release))}
			}

			opts := s3.CopyObjectInput{
				Bucket:            &config.Bucket,
				CopySource:        aws.String(fmt.Sprintf("%v/%v", config.Bucket, key)),
				Key:               &key,
				ACL:               &config.ACL,
				CacheControl:      &config.CacheControl,
				MetadataDirective: aws.String("REPLACE"),
				Metadata:          meta,
				ContentEncoding:   obj.ContentEncoding,
				ContentType:       obj.ContentType,
			}

			_, err := s3c.CopyObject(&opts)
			if err != nil {
				log.Errorf("Updating failed: %v", err)
			}
		}
	}

	if len(removeFiles) > 0 {
		log.Infoln("Deleting remote files...")

		for key := range removeFiles {
			log.Debugf("Delete %s...", key)

			_, err := s3c.DeleteObject(&s3.DeleteObjectInput{
				Bucket: &config.Bucket,
				Key:    &key,
			})

			if err != nil {
				log.Errorf("Delete failed: %v", err)
			}
		}
	}
}

func confirm(message string) bool {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("%s [y/N]: ", message)

	response, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}

	response = strings.ToLower(strings.TrimSpace(response))

	return response == "y" || response == "yes"
}

func keep(c *tConfig, i int) bool {
	return i+c.Keep > c.Release
}
