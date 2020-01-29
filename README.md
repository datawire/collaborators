# collaborators.go

Print a summary of the permissions of all the repositories in the
"datawire" GitHub organization.

Usage:

 1. Be an "owner" of the "datawire" GitHub organization.
 2. Go to https://github.com/settings/tokens and create a Personal
    Access Token that has the `admin:org` permission.
 3. `export GH_TOKEN=that_personal_access_token`
 4. `go run ./collaborators.go`  Be patient, it won't display anything
    until it's all done; and it takes a little over 1 minute to run.

The output has most-recently-modified repos at the top, and repos that
haven't been modified in a long time at the bottom.
