# Canvas Sync

`canvas-sync` downloads files from a [Canvas by Instructure](https://www.instructure.com/canvas) web server and creates a similar folder structure on your local computer.

## Setup

### Access Token

`canvas-sync` uses the [Canvas LMS API](https://canvas.instructure.com/doc/api/) to download files from the Canvas server.
In order to authenticate with the server an access token must be generated. An access tokens allows third-party applications such as `canvas-sync` to access Canvas resources on your behalf.

To create a token:

1. On Canvas, go to "Account" followed by "Settings.
2. Under "Approved integrations", click on the "New access token" button.
3. Put canvas-sync as the token's purpose and then click on the "Generate token" button.
4. Copy and paste the token in the configuration file, described below.

### Configuration File

Next, create a JSON file in your home directory called `.canvassync.json` like the following

```
{
    "url": "https://canvas.northwestern.edu",
    "token": "AUTHENTICATION TOKEN GOES HERE",
    "directory": "D:/Canvas",
    "ignored_courses": [
        178029,
        178124,
        145482
    ]
}
```
where:

* `url` is the URL of your Canvas server;
* `token` is the authentication token created as described in the previous section;
* `directory` is the path to the directory on the local file system where you want Canvas files to be synced to;
* and `ignored_courses` is a list of course IDs that you do not want to be synced.
