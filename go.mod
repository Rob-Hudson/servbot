module servbot

go 1.21.6

require (
	github.com/alecthomas/kong v0.8.1
	golang.zx2c4.com/irc v0.0.0-20211018023802-6d08d74c58ff
)

require github.com/BurntSushi/toml v1.3.2
replace golang.zx2c4.com/irc => ./irc-go
