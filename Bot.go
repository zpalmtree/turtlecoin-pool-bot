package main

import (
    "github.com/bwmarrin/discordgo"
    "fmt"
    "os"
    "os/signal"
    "syscall"
    "bufio"
    "strings"
    "net/http"
    "encoding/json"
    "io/ioutil"
    "regexp"
    "strconv"
    "errors"
    "sort"
    "time"
    "compress/flate"
    "bytes"
)

const poolsJSON string = "https://raw.githubusercontent.com/turtlecoin/" +
                         "turtlecoin-pools-json/master/turtlecoin-pools.json"

/* You will need to change this to the channel ID of the pools channel. To
   get this, go here - https://stackoverflow.com/a/41515544/8737306 */
const poolsChannel string = "426881205263269900"

/* The amount of blocks a pool can vary from the others before we notify */
const poolMaxDifference int = 5

/* How often we check the pools */
const poolRefreshRate time.Duration = time.Second * 30

/* The data type we parse our json into */
type Pool struct {
    Api string `json:"url"`
}

/* Map of pool name to pool api */
type Pools map[string]Pool

/* Info about every pool */
type PoolsInfo struct {
    pools               []PoolInfo
    medianHeight        int
    heightLastUpdated   time.Time
    warned              bool
}

/* Info about an individual pool */
type PoolInfo struct {
    url                 string
    api                 string
    claimed             bool
    userID              string
    height              int
    heightFailCounter   int
    apiFailCounter      int
    warnedApi           bool
    warnedHeight        bool
}

var globalInfo PoolsInfo

func main() {
    err := setup()

    if err != nil {
        return
    }
    
    discord, err := startup()
    
    if err != nil {
        return
    }

    fmt.Println("Bot started!")

    /* Update the height and pools in the background */
    go heightWatcher(discord)
    go poolUpdater()

    sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
    <-sc

    fmt.Println("Shutdown requested.")

    discord.Close()

    fmt.Println("Shutdown.")
}

func setup() error {
    pools, err := getPools()

    if err != nil {
        return err
    }

    claims, err := getClaims()

    poolInfo := make([]PoolInfo, 0)

    /* Populate each pool with their info */
    for url, pool := range pools {
        var p PoolInfo
        p.url = url
        p.api = pool.Api

        /* Has the pool been claimed */
        if val, ok := claims[p.url]; ok {
            p.claimed = true
            p.userID = val
        } else {
            p.claimed = false
        }

        p.apiFailCounter = 0
        p.heightFailCounter = 0

        p.warnedApi = false
        p.warnedHeight = false

        poolInfo = append(poolInfo, p)
    }

    /* Update the global struct */
    globalInfo.pools = poolInfo
    globalInfo.warned = false

    populateHeights()
    updateMedianHeight()

    return nil
}

func writeClaims() {
    file, err := os.Create("claims.txt")

    if err != nil {
        fmt.Println("Failed to open file! Error:", err)
        return
    }

    defer file.Close()

    for _, v := range globalInfo.pools {
        if v.claimed {
            file.WriteString(fmt.Sprintf("%s:%s\n", v.url, v.userID))
        }
    }

    file.Sync()
}

func getClaims() (map[string]string, error) {
    claims := make(map[string]string)

    /* File exists */
    if _, err := os.Stat("claims.txt"); err == nil {
        file, err := os.Open("claims.txt")

        defer file.Close()

        if err != nil {
            return claims, err
        }

        scanner := bufio.NewScanner(file)
    
        re := regexp.MustCompile("(.+):(\\d+)")

        for scanner.Scan() {
            matches := re.FindStringSubmatch(scanner.Text())

            if len(matches) < 3 {
                fmt.Println("Failed to parse claim!")
                continue
            }

            claims[matches[1]] = matches[2]
        }

        if err := scanner.Err(); err != nil {
            fmt.Println("Failed to read claims.txt! Error:", err)
            return claims, err
        }
    }

    return claims, nil
}

func checkForDownedApis(s *discordgo.Session) {
    msg := fmt.Sprintf("```It looks like some pools api's have gone " +
                       "down!\nMedian pool height: %d\n\n", 
                       globalInfo.medianHeight)

    poolOwners := make([]string, 0)
    sendMessage := false

    for index, _ := range globalInfo.pools {
        v := &globalInfo.pools[index]

        if v.height == 0 {
            /* Maybe their api momentarily went down or something, don't
               instantly ping */
            if v.apiFailCounter <= 2 {
                v.apiFailCounter++
            /* Only warn the user once */
            } else if !v.warnedApi {
                sendMessage = true;
                v.warnedApi = true

                msg += fmt.Sprintf("%-25s %d\n", v.url, v.height)

                if v.claimed {

                    alreadyExists := false

                    for _, owner := range poolOwners {
                        if owner == v.userID {
                            alreadyExists = true
                        }
                    }

                    /* Don't multi add the username if they own multiple 
                       pools */
                    if !alreadyExists {
                        poolOwners = append(poolOwners, v.userID)
                    }
                }
            }
        } else {
            v.apiFailCounter = 0
            v.warnedApi = false
        }
    }

    msg += "```"

    if sendMessage {
        for _, owner := range poolOwners {
            msg += fmt.Sprintf("<@%s> ", owner)
        }

        s.ChannelMessageSend(poolsChannel, msg)
    }
}

func checkForBehindChains(s *discordgo.Session) {
    msg := fmt.Sprintf("```It looks like some pools are stuck, " +
                       "forked, or behind!\nMedian pool height: %d\n\n", 
                       globalInfo.medianHeight)

    poolOwners := make([]string, 0)
    sendMessage := false

    for index, _ := range globalInfo.pools {
        v := &globalInfo.pools[index]

        if ((v.height > globalInfo.medianHeight +
                        poolMaxDifference ||
             v.height < globalInfo.medianHeight -
                        poolMaxDifference) &&
             v.height != 0) {
            /* Maybe their api momentarily went down or something, don't
               instantly ping */
            if v.heightFailCounter <= 2 {
                v.heightFailCounter++
            /* Only warn the user once */
            } else if !v.warnedHeight {
                sendMessage = true;
                v.warnedHeight = true

                msg += fmt.Sprintf("%-25s %d\n", v.url, v.height)

                if v.claimed {

                    alreadyExists := false

                    for _, owner := range poolOwners {
                        if owner == v.userID {
                            alreadyExists = true
                        }
                    }

                    /* Don't multi add the username if they own multiple 
                       pools */
                    if !alreadyExists {
                        poolOwners = append(poolOwners, v.userID)
                    }
                }
            }
        } else {
            v.heightFailCounter = 0
            v.warnedHeight = false
        }
    }

    msg += "```"

    if sendMessage {
        for _, owner := range poolOwners {
            msg += fmt.Sprintf("<@%s> ", owner)
        }

        s.ChannelMessageSend(poolsChannel, msg)
    }

}

func checkForStuckChain(s *discordgo.Session) {
    timeSinceLastBlock := time.Since(globalInfo.heightLastUpdated)

    /* Alert if the chain has been stuck for longer than 5 minutes */
    if timeSinceLastBlock > (time.Minute * 5) {
        s.ChannelMessageSend(poolsChannel,
                             fmt.Sprintf("It looks like the chain is " +
                                         "stuck! The last block was " +
                                         "found %d minutes ago!", 
                                         int(timeSinceLastBlock.Minutes())))
        globalInfo.warned = true
    } else {
        globalInfo.warned = false
    }
}

func heightWatcher(s *discordgo.Session) {
    for {
        time.Sleep(poolRefreshRate)

        populateHeights()
        updateMedianHeight()

        checkForStuckChain(s)
        checkForBehindChains(s)
        checkForDownedApis(s)
    }
}

/* Update the pools json every hour */
func poolUpdater() {
    for {
        time.Sleep(time.Hour)

        pools, err := getPools()

        if err != nil {
            fmt.Println("Failed to update pools info! Error:", err)
            return
        }

        claims, err := getClaims()

        poolInfo := make([]PoolInfo, 0)

        /* Populate each pool with their info */
        for url, pool := range pools {
            var p PoolInfo
            p.url = url
            p.api = pool.Api

            /* Has the pool been claimed */
            if val, ok := claims[p.url]; ok {
                p.claimed = true
                p.userID = val
            } else {
                p.claimed = false
            }

            p.apiFailCounter = 0
            p.heightFailCounter = 0

            p.warnedApi = false
            p.warnedHeight = false

            poolInfo = append(poolInfo, p)

            /* Update it with the local pool info if it exists */
            for _, localPool := range globalInfo.pools {
                if p.url == localPool.url {
                    p.heightFailCounter = localPool.heightFailCounter
                    p.apiFailCounter = localPool.apiFailCounter
                    p.warnedApi = localPool.warnedApi
                    p.warnedHeight = localPool.warnedHeight
                    p.height = localPool.height
                    break
                }
            }
        }

        /* Update the global struct */
        globalInfo.pools = poolInfo
        populateHeights()
        updateMedianHeight()
    }
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
    /* Ignore our own messages */
    if m.Author.ID == s.State.User.ID {
        return
    }

    /* Only send messages in the pool channel */
    if m.ChannelID != poolsChannel {
        return
    }

    if m.Content == ".heights" {
        heightsPretty := "```\nAll known pool heights:\n\n"

        for _, v := range globalInfo.pools {
            heightsPretty += fmt.Sprintf("%-25s %d\n", v.url, v.height)
        }

        heightsPretty += "```"

        s.ChannelMessageSend(m.ChannelID, heightsPretty)

        return
    }

    if m.Content == ".help" {
        helpCommand := fmt.Sprintf("```\nAvailable commands:\n\n" +
                   ".help           Display this help message\n" +
                   ".heights        Display the heights of all known pools\n" +
                   ".height         Display the median height of all pools\n" +
                   ".height <pool>  Display the height of <pool>\n" +
                   ".claim <pool>   Claim the pool <pool> as your pool so " +
                                   "you can be sent notifications```")


        s.ChannelMessageSend(m.ChannelID, helpCommand)

        return
    }

    if m.Content == ".height" {
        s.ChannelMessageSend(m.ChannelID, 
                             fmt.Sprintf("```Median pool height:\n\n%d```", 
                                         globalInfo.medianHeight))

        return
    }

    if strings.HasPrefix(m.Content, ".height") {
        message := strings.TrimPrefix(m.Content, ".height")
        /* Remove first char - probably a space but should make sure */
        message = message[1:]

        for _, v := range globalInfo.pools {
            if v.url == message {
                s.ChannelMessageSend(m.ChannelID,
                                     fmt.Sprintf("```%s pool height:\n\n%d```",
                                                 v.url, v.height))
                return
            }
        }

        s.ChannelMessageSend(m.ChannelID,
                             fmt.Sprintf("Couldn't find pool %s - type " +
                                         "`.heights` to view all known pools.",
                                         message))

        return
    }

    if m.Content == ".claim" {
        s.ChannelMessageSend(m.ChannelID,
                             "You must specify a pool to claim!\nType " +
                             "`.heights` to list all pools.")

        return
    }

    if strings.HasPrefix(m.Content, ".claim") {
        message := strings.TrimPrefix(m.Content, ".claim")
        message = message[1:]

        for index, _ := range globalInfo.pools {
            v := &globalInfo.pools[index]

            if v.url == message {
                /* Pool has already been claimed */
                if v.claimed {
                    user, err := s.User(v.userID)

                    if err != nil {
                        fmt.Println("Couldn't find user! Error:", err)
                        return
                    }

                    s.ChannelMessageSend(m.ChannelID,
                                         fmt.Sprintf("%s has already been " +
                                                     "claimed by %s!", v.url,
                                                     user.Username))
                    
                    return
                /* Otherwise insert into the map */
                } else {
                    v.claimed = true
                    v.userID = m.Author.ID
                    
                    s.ChannelMessageSend(m.ChannelID,
                                         fmt.Sprintf("You have claimed %s!", 
                                                     v.url))

                    writeClaims()

                    return
                }
            }
        }

        s.ChannelMessageSend(m.ChannelID,
                             fmt.Sprintf("Could find pool %s - type " +
                                         "`.heights` to view all known pools.",
                                         message))

        return
    }
}

func getValues(heights map[string]int) []int {
    values := make([]int, 0)

    for _, v := range heights {
        values = append(values, v)
    }

    return values
}

func updateMedianHeight() {
    heights := make([]int, 0)

    for _, v := range globalInfo.pools {
        heights = append(heights, v.height)
    }

    median := median(heights)

    if median != globalInfo.medianHeight {
        globalInfo.medianHeight = median
        globalInfo.heightLastUpdated = time.Now()
    }
}

func median(heights []int) int {
    sort.Ints(heights)

    half := len(heights) / 2
    median := heights[half]

    if len(heights) % 2 == 0 {
        median = (median + heights[half-1]) / 2
    }

    return median
}

func populateHeights() {
    for index, _ := range globalInfo.pools {
        /* Range takes a copy of the values, we need to directly access */
        v := &globalInfo.pools[index]

        height, err := getPoolHeight(v.api)

        if err == nil {
            v.height = height
        } else {
            v.height = 0
        }
    }
}

func getPoolHeight (apiURL string) (int, error) {
    statsURL := apiURL + "stats"

    resp, err := http.Get(statsURL)

    if err != nil {
        fmt.Printf("Failed to download stats from %s! Error: %s\n", 
                    statsURL, err)
        return 0, err
    }

    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)

    if err != nil {
        fmt.Printf("Failed to download stats from %s! Error: %s\n",
                    statsURL, err)
        return 0, err
    }

    /* Some servers (Looking at you us.turtlepool.space! send us deflate'd
       content even when we didn't ask for it - uncompress it */
    if resp.Header.Get("Content-Encoding") == "deflate" {
        body, err = ioutil.ReadAll(flate.NewReader(bytes.NewReader(body)))

        if err != nil {
            fmt.Println("Failed to deflate response from", statsURL)
            return 0, err
        }
    }

    re := regexp.MustCompile(".*\"height\":(\\d+).*")

    height := re.FindStringSubmatch(string(body))

    if len(height) < 2 {
        fmt.Println("Failed to parse height from", statsURL)
        return 0, errors.New("Couldn't parse height")
    }

    i, err := strconv.Atoi(height[1])

    if err != nil {
        fmt.Println("Failed to convert height into int! Error:", err)
        return 0, err
    }

    return i, nil
}

func getPools() (Pools, error) {
    var pools Pools

    resp, err := http.Get(poolsJSON)

    if err != nil {
        fmt.Println("Failed to download pools json! Error:", err)
        return pools, err
    }

    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)

    if err != nil {
        fmt.Println("Failed to download pools json! Error:", err)
        return pools, err
    }

    if err := json.Unmarshal(body, &pools); err != nil {
        fmt.Println("Failed to parse pools json! Error:", err)
        return pools, err
    }

    return pools, nil
}

func startup() (*discordgo.Session, error) {
    var discord *discordgo.Session

    token, err := getToken()

    if err != nil {
        fmt.Println("Failed to get token! Error:", err)
        return discord, err
    }

    discord, err = discordgo.New("Bot " + token)

    if err != nil {
        fmt.Println("Failed to init bot! Error:", err)
        return discord, err
    }

    discord.AddHandler(messageCreate)

    err = discord.Open()

    if err != nil {
        fmt.Println("Error opening connection! Error:", err)
        return discord, err
    }

    fmt.Println("Connected to discord!")

    return discord, nil
}

func getToken() (string, error) {
    file, err := os.Open("token.txt")

    defer file.Close()

    if err != nil {
        return "", err
    }

    reader := bufio.NewReader(file)

    line, err := reader.ReadString('\n')

    if err != nil {
        return "", err
    }

    line = strings.TrimSuffix(line, "\n")

    return line, nil
}
