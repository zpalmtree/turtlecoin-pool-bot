# turtlecoin-pool-bot

This bot hangs out in your discord server, and lets you know if mining pools are falling behind/ahead/stuck, or if their API has gone down. It will also let you know if a block hasn't been found in the last 5 minutes.

## Prerequisites

* A semi recent version of Go. The one provided by the default Ubuntu repositories is too old (Surprise surprise). You can use the binary distributions from the Golang website successfully on Ubuntu.

## Setup

* Go [here](https://discordapp.com/developers/applications/me#top) to make a bot.
* Give your bot a name, and then click `Create Application`.
* Scroll down to `Create a Bot User` and click that.
* Note down the `Client ID` for later.
* Now you can get your bot token by clicking `click to reveal` in the bot user section.
* Create a file `token.txt` with your token in.
* **Don't reveal this token to anyone!**
* Next you need to get the Channel ID you want the bot to run in.
* In Discord, follow these steps-

   1. Click on `User Settings`(small gear icon to right of name in the bottom left) 
   
   2. Click on `Appearance` 
   
   3. Enable `Developer Mode`.
   
* Right click on the Discord channel you want the bot to work in, and press `Copy ID`.
* Open up `Bot.go`, and replace the value of `poolsChannel` with the ID you just copied.
* Edit this link, replacing the string of numbers after `client_id=` with the Client ID you noted down earlier.
`https://discordapp.com/oauth2/authorize?client_id=426572589977042946&scope=bot&permissions=3072`
* Open said link and choose the server you wish to add the bot to. You must have `Manage Server` permissions.

## Building

* `go get github.com/bwmarrin/discordgo`
* `go build Bot.go`

## Running

* `./Bot`

## Usage

There are a few commands once the bot is running:

* /help - Display the help message
* /heights - Display the heights of all known pools
* /status - An alias for /heights
* /height - Display the median height
* /height \<pool\> - Display the height of \<pool\>
* /forked - Display any forked pools
* /lastfound - Display time since the last block was found
* /watch \<pool\> - Watch the pool \<pool\> so you can be sent notifications
* /unwatch \<pool\> - Stop watching the pool \<pool\> so you are no longer send notifications
