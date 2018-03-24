# turtlecoin-pool-bot

## Prerequisites

* A semi recent version of go. The one provided by the default ubuntu repositories is too old (Surprise surprise). You can use the binary distributions from the golang website successfully on ubuntu.

## Setup

* Go [here](https://discordapp.com/developers/applications/me#top) to make a bot.
* Give your bot a name, and then click `Create Application`.
* Scroll down to `Create a bot user` and click that.
* Now you can get your bot token by clicking `click to reveal` in the bot user section.
* Create a file `token.txt` with your token in.
* Don't reveal this token to anyone!
* Next you need to get the channel ID you want the bot to run in.
* In discord, enable settings -> appearance -> enable developer mode.
* Right click on the discord channel you want the bot to work in, and press Copy ID.
* Open up Bot.go, and replace the value of `poolsChannel` with the ID you just copied.

## Building

* `go get github.com/bwmarrin/discordgo`
* `go build Bot.go`

## Running

* `./Bot`
