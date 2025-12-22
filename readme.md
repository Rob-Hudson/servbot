# Servbot
Servbot is a file server for IRC. It allows people to download files through DCC.

## Requirements
You need one port per active simultaneous download.
If all ports are used, newly requested files will be queued.

## Usage
* Build with `go build`.
* Create `config.toml`. You can use `config.toml.example` as a template.
* Generate your file lists with `servbot genlist`. If `header.txt` exists, it will be prepended to the list.
* Run with `servbot serve`. Watch the log for things going wrong.

Once the lists are generated, there will be two lists.
`filelists/list` is the internal list, and will be searched when someone downloads something.
`filelists/userlist.zip` is the list sent to users when requested.

Note: If you put `/home/user/path` in `listpaths`, that directory name will show up in the userlist.
To avoid that, make a directory of symlinks somewhere else and point at them.

## Multiple lists
If you want multiple lists, add a lists item to the config file (see example).
To download one of these lists, send @prefix-listname.
Note that all directories in the sub-lists must be in listpaths, otherwise downloads won't work.

## IRC control
If `controller` is set in the config file, anyone with that nickname can control the bot through IRC messages.
The available messages are:

* shutdown - shuts down the bot.
* queue - shows the queue.
* clear - clears the queue.
* stats - shows number of files and sum total of bytes sent, as of the prior midnight.
* transfers - shows the map of in-progress transfers to nicknames.
* genlist - starts a refresh of the file list. The bot will tell you what happened. For more complete output, check the log.
* ignore add \<pattern\> [duration] [reason] - adds an ignore entry.
* ignore del \<pattern\> - removes an ignore entry.
* ignore list - shows all active ignores.
* ignore clean - removes expired ignores.

### Ignore patterns
Patterns use the format `nick!user@host` with wildcard support (`*` matches any characters, `?` matches a single character).

Examples:
* `*!*@bad.host.com` - ignore everyone from a specific host
* `*!*@192.168.1.*` - ignore a subnet
* `*!*@*.evil.net` - ignore all subdomains
* `*!baduser@*` - ignore a specific username from any host
* `spammer.net` - shorthand for `*!*@spammer.net`

### Ignore durations
* `10S` - 10 seconds
* `10M` - 10 minutes
* `24H` - 24 hours
* `7D` - 7 days
* `2W` - 2 weeks
* `perm` or omit - permanent

Ignores are saved to `ignores.json` and persist across restarts.

## TLS verification
If port is 6697, the bot will try to connect with TLS.
If the normal verification fails, set `insecure_skip_verify` to `true` in the configuration file.
When the bot tries to connect, it will print the fingerprints. Set `tls_fingerprints` to a list of fingerprints you want to consider verified.
## Starting the bot
There is a startup directory containing a systemd service file. Place it in your systemd  user directory, by issuing the following commands:
mkdir -pv ~/.config/systemd/user
cp ./startup/servbot.service ~/config/systemd/user
Edit the file with your username and group. Then, optionally type the following:
systemctl --user enable servbot
Or, if you do not wish to have the bot launch on startup, type:
systemctl --user start servbot

To monitor the service, use the following commands:

Check status
systemctl --user status servvbot
Check the bot's log
journalctl --user -u servbot.service -f
Stop service
systemctl --user stop servbot.service
Restart service
systemctl --user restart servbot.service
