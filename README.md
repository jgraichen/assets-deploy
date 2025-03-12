# assets-deploy

Deploy static assets to an S3 bucket.

## Features

* Upload assets to an S3 bucket
* Use public-read ACL
* Determine Content-Type and Content-Encoding from file extensions
* Configure a Cache-Control policy
* Update existing files in-place
* Remove old assets but keep a some previous releases

## Usage

```console
> assets-deploy --bucket assets-prod --source public/assets --release 2142 --keep 1
INFO Release: 7
INFO Found 3 files
INFO Reading bucket for existing files...

An execution plan has been generated and is shown below.
Actions are indicated with the following symbols:
  + Upload new file
  ~ Update remote file in-place
  - Delete remote file

Current execution plan:
    application-23fe3a.css
    application-23fe3a.css.gz
  + application-437f37.css
  + application-437f37.css.gz
  ~ logo-3aef33.png
  - old-34de30.css
  - old-34de30.css.gz

Execute? [y/N]: y

```

See all options with `assets-deploy --help`.

## Attention

* Files with identical names are expected to be identical. Using fingerprinting is required.

* Release number defaults to `BUILD_NUMBER` environment variable and must be an integer.

* Credentials are read from `~/.aws/credentials` or environment variables.

* Compressed files (`.gz`, `.br`, `.zz`) are uploaded as the uncompressed content type and appropriate Content-Encoding header. They are expected to be fetched by a proxy based on the clients Accept-Encoding header.

## License

MIT
