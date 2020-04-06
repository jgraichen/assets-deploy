package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	_log "log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/bmatcuk/doublestar"
	"github.com/logrusorgru/aurora"
	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
)

type tConfig struct {
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

type tObject struct {
	CacheControl    *string
	ContentEncoding *string
	ContentType     *string
	Release         int
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

	flag.BoolVar(&config.Deploy, "deploy", true, "Deploy new assets")
	flag.BoolVar(&config.Clean, "clean", true, "Remove old assets")
	flag.BoolVar(&config.Debug, "debug", false, "Debug logging")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Dry-run")
	flag.BoolVar(&config.Force, "force", false, "")
	flag.BoolVar(&config.Yes, "yes", false, "Do not ask for confirmation")
	flag.BoolVar(&config.Quiet, "quiet", false, "Do not say what I'm doing")
	flag.IntVar(&config.Release, "release", defaultRelease, "Release number")
	flag.IntVar(&config.Keep, "keep", 10, "Number of releases to keep")
	flag.StringVar(&config.Pattern, "pattern", "**/*", "Assets to deploy")
	flag.StringVar(&config.Source, "source", ".", "Source directory")
	flag.StringVar(&config.Bucket, "bucket", "", "S3 Bucket")
	flag.StringVar(&config.CacheControl, "cache-control", "public,immutable,max-age=31536000", "")
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

	localFiles := make(map[string]os.FileInfo)

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

		localFiles[rel] = info
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

	s2s := session.New(s3Config)
	s3c := s3.New(s2s)

	remoteFiles := make(map[string]tObject)

	listOpts := s3.ListObjectsV2Input{Bucket: &config.Bucket}
	err = s3c.ListObjectsV2Pages(&listOpts, func(page *s3.ListObjectsV2Output, last bool) bool {
		for _, item := range page.Contents {
			obj, err := s3c.HeadObject(&s3.HeadObjectInput{Bucket: &config.Bucket, Key: item.Key})
			if err != nil {
				log.Debugf("Skip %s: %v", *item.Key, err)
			}

			release := 0
			if val, ok := obj.Metadata["release"]; ok && val != nil {
				release, _ = strconv.Atoi(*val)
			}

			remoteFiles[*item.Key] = tObject{
				Release:         release,
				CacheControl:    obj.CacheControl,
				ContentType:     obj.ContentType,
				ContentEncoding: obj.ContentEncoding,
			}
		}

		return true
	})

	if err != nil {
		log.Fatalln(err)
	}

	var allFiles []string

	newFiles := make(map[string]bool)
	changedFiles := make(map[string]bool)
	for file := range localFiles {
		allFiles = append(allFiles, file)

		if obj, ok := remoteFiles[file]; ok {
			needUpdate := config.Force

			if obj.CacheControl != nil {
				if *obj.CacheControl != config.CacheControl {
					log.Warnf("%s: Different Cache-Control: %s", file, *obj.CacheControl)
					needUpdate = true
				}
			} else {
				log.Warnf("%s: Missing Cache-Control", file)
				needUpdate = true
			}

			if needUpdate {
				changedFiles[file] = true
			} else {
				log.Debugf("%s: up-to-date", file)
			}
		} else {
			newFiles[file] = true
		}
	}

	removeFiles := make(map[string]bool)
	keepFiles := make(map[string]bool)
	for file, obj := range remoteFiles {
		if _, ok := localFiles[file]; !ok {
			allFiles = append(allFiles, file)

			if obj.Release < (config.Release - config.Keep) {
				removeFiles[file] = true
			} else {
				keepFiles[file] = true
			}
		}
	}

	sort.Strings(allFiles)

	if !(config.Quiet && config.Yes) {
		fmt.Println()
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
			if _, ok := changedFiles[file]; ok {
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
	}

	if config.DryRun {
		os.Exit(0)
	}

	if !config.Yes && !confirm("Execute?") {
		fmt.Println("Abort.")
		os.Exit(0)
	}

	// TODO: Uploading files
	// TODO: Updating files
	// TODO: Removing files
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
