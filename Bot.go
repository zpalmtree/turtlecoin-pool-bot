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
    "crypto/tls"
)

const poolsJSON string = "https://raw.githubusercontent.com/turtlecoin/" +
                         "turtlecoin-pools-json/master/turtlecoin-pools.json"

/* You will need to change this to the channel ID of the pools channel. To
   get this, go here - https://stackoverflow.com/a/41515544/8737306 */
const poolsChannel string = "430779541921726465"

/* The amount of blocks a pool can vary from the others before we notify */
const poolMaxDifference int = 5

/* How often we check the pools */
const poolRefreshRate time.Duration = time.Second * 30

/* We ignore some pools from the forked/api down message because they are
   constantly up and down and are quite noisy */
var ignoredPools = []string { "turtle.coolmining.club" }

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
    apiFailCounter      int
    warnedApi           bool
    warnedHeight        bool
    timeLastFound       time.Time
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
    downMsg := fmt.Sprintf("```It looks like some pools api's have gone " +
                           "down!\nMedian pool height: %d\n\n", 
                            globalInfo.medianHeight)

    recoverMsg := fmt.Sprintf("```Some pools api's have recovered!\nMedian " +
                              "pool height: %d\n\n", globalInfo.medianHeight)

    poolOwners := make([]string, 0)
    sendDownMessage := false
    sendRecoverMessage := false

    for index, _ := range globalInfo.pools {
        v := &globalInfo.pools[index]

        ignore := false

        /* Some pools really spam the output. Ignore them. */
        for _, v1 := range ignoredPools {
            if v1 == v.url {
                ignore = true
                break
            }
        }

        if ignore {
            continue
        }

        if v.height == 0 {
            /* Maybe their api momentarily went down or something, don't
               instantly ping */
            if v.apiFailCounter <= 2 {
                v.apiFailCounter++
            /* Only warn the user once */
            } else if !v.warnedApi {
                sendDownMessage = true;
                v.warnedApi = true

                downMsg += fmt.Sprintf("%-25s %d\n", v.url, v.height)

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
            if v.warnedApi {
                sendRecoverMessage = true
                recoverMsg += fmt.Sprintf("%-25s %d\n", v.url, v.height)
            }

            v.apiFailCounter = 0
            v.warnedApi = false
        }
    }

    downMsg += "```"
    recoverMsg += "```"

    if sendDownMessage {
        for _, owner := range poolOwners {
            /* Ping the owners */
            downMsg += fmt.Sprintf("<@%s> ", owner)
        }

        s.ChannelMessageSend(poolsChannel, downMsg)
    }

    if sendRecoverMessage {
        s.ChannelMessageSend(poolsChannel, recoverMsg)
    }
}

func checkForBehindChains(s *discordgo.Session) {
    downMsg := fmt.Sprintf("```It looks like some pools are stuck, " +
                           "forked, or behind!\nMedian pool height: %d\n\n", 
                            globalInfo.medianHeight)

    recoverMsg := fmt.Sprintf("```Some pools have recovered!\nMedian pool " +
                              "height: %d\n\n", globalInfo.medianHeight)

    poolOwners := make([]string, 0)

    sendDownMessage := false
    sendRecoverMessage := false

    for index, _ := range globalInfo.pools {
        v := &globalInfo.pools[index]

        ignore := false

        /* Some pools really spam the output. Ignore them. */
        for _, v1 := range ignoredPools {
            if v1 == v.url {
                ignore = true
                break
            }
        }

        if ignore {
            continue
        }

        if ((v.height > globalInfo.medianHeight +
                        poolMaxDifference ||
             v.height < globalInfo.medianHeight -
                        poolMaxDifference) &&
             v.height != 0) {
            if !v.warnedHeight {
                sendDownMessage = true;
                v.warnedHeight = true

                downMsg += fmt.Sprintf("%-25s %d\n", v.url, v.height)

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
        /* Don't print recover message if api has gone down as well */
        } else if v.height != 0 {
            if v.warnedHeight {
                sendRecoverMessage = true
                recoverMsg += fmt.Sprintf("%-25s %d\n", v.url, v.height)
            }

            v.warnedHeight = false
        }
    }

    downMsg += "```"
    recoverMsg += "```"

    if sendDownMessage {
        for _, owner := range poolOwners {
            /* Ping the owners */
            downMsg += fmt.Sprintf("<@%s> ", owner)
        }

        s.ChannelMessageSend(poolsChannel, downMsg)
    }

    if sendRecoverMessage {
        s.ChannelMessageSend(poolsChannel, recoverMsg)
    }

}

func checkForStuckChain(s *discordgo.Session) {
    timeSinceLastBlock := time.Since(globalInfo.heightLastUpdated)

    /* Alert if the chain has been stuck for longer than 5 minutes */
    if timeSinceLastBlock > (time.Minute * 5) {
        /* Only warn once */
        if !globalInfo.warned {
            s.ChannelMessageSend(poolsChannel,
                                 fmt.Sprintf("It looks like the chain is " +
                                             "stuck! The last block was " +
                                             "found %d minutes ago!", 
                                             int(timeSinceLastBlock.Minutes())))
            globalInfo.warned = true
        }
    /* We have already warned, so print out a recovery message */
    } else if globalInfo.warned {
        globalInfo.warned = false
        s.ChannelMessageSend(poolsChannel,
                             fmt.Sprintf("The chain appears to have " +
                                         "recovered. The last block was " +
                                         "found %d minutes ago.",
                                         int(timeSinceLastBlock.Minutes())))
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

            p.warnedApi = false
            p.warnedHeight = false

            /* Update it with the local pool info if it exists */
            for _, localPool := range globalInfo.pools {
                if p.url == localPool.url {
                    p.apiFailCounter = localPool.apiFailCounter
                    p.warnedApi = localPool.warnedApi
                    p.warnedHeight = localPool.warnedHeight
                    p.height = localPool.height
                    p.timeLastFound = localPool.timeLastFound
                    break
                }
            }

            poolInfo = append(poolInfo, p)
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

    channel, err := s.Channel(m.ChannelID)

    if err != nil {
        fmt.Println("Failed to get channel! Error:", err)
        return
    }

    member, err := s.GuildMember(channel.GuildID, m.Author.ID)

    if err != nil {
        fmt.Println("Failed to get guild member! Error:", err)
        return
    }

    isColouredName := false

    for _, v := range member.Roles {
        role, err := s.State.Role(channel.GuildID, v)

        if err != nil {
            fmt.Println("Failed to get role! Error:", err)
            return
        }

        if role.Name == "NINJA" || role.Name == "dev-turtle" ||
           role.Name == "helper" || role.Name == "FOOTCLAN" ||
           role.Name == "contributor" || role.Name == "guerilla" ||
           role.Name == "service operator" {
            isColouredName = true
            break
        }
    }

    /* Only allowed privileged users or those in stats channel to use bot */
    if !isColouredName && m.ChannelID != poolsChannel {
        return
    }

    if m.Content == "/heights" {
        heightsPretty := "```\nPool                      Height     Block " +
                         "Last Found\n\n"

        for _, v := range globalInfo.pools {
            mins := int(time.Since(v.timeLastFound).Minutes())
            hours := int(time.Since(v.timeLastFound).Hours())

            if v.timeLastFound.IsZero() {
                heightsPretty += fmt.Sprintf("%-25s %-11dNever\n", 
                                             v.url, v.height)
            } else if mins < 60 {
                heightsPretty += fmt.Sprintf("%-25s %-11d%d minutes ago\n",
                                             v.url, v.height, mins)
            } else if hours < 24 {
                heightsPretty += fmt.Sprintf("%-25s %-11d%d hours ago\n",
                                             v.url, v.height, hours)
            } else {
                heightsPretty += fmt.Sprintf("%-25s %-11d%d days ago\n",
                                             v.url, v.height, int(hours / 24))
            }
        }

        heightsPretty += "```"

        s.ChannelMessageSend(m.ChannelID, heightsPretty)

        return
    }

    if m.Content == "/help" {
        helpCommand := fmt.Sprintf("```\nAvailable commands:\n\n" +
                   "/help           Display this help message\n" +
                   "/heights        Display the heights of all known pools\n" +
                   "/height         Display the median height of all pools\n" +
                   "/height <pool>  Display the height of <pool>\n" +
                   "/claim <pool>   Claim the pool <pool> as your pool so " +
                                   "you can be sent notifications```")


        s.ChannelMessageSend(m.ChannelID, helpCommand)

        return
    }

    if m.Content == "/height" {
        s.ChannelMessageSend(m.ChannelID, 
                             fmt.Sprintf("```Median pool height:\n\n%d```", 
                                         globalInfo.medianHeight))

        return
    }

    if strings.HasPrefix(m.Content, "/height") {
        message := strings.TrimPrefix(m.Content, "/height")
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
                                         "`/heights` to view all known pools.",
                                         message))

        return
    }

    if m.Content == "/claim" {
        if m.ChannelID == poolsChannel {
            s.ChannelMessageSend(m.ChannelID,
                                 "You must specify a pool to claim!\nType " +
                                 "`/heights` to list all pools.")

        } else {
            s.ChannelMessageSend(m.ChannelID,
                                 "You can only use this command in the " +
                                 "#stats channel!")
        }

        return
    }

    if strings.HasPrefix(m.Content, "/claim") {
        if m.ChannelID == poolsChannel {
            message := strings.TrimPrefix(m.Content, "/claim")
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
                                             fmt.Sprintf("%s has already " +
                                                         "been claimed by %s!", 
                                                         v.url,
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
                                             "`.heights` to view all known " +
                                             "pools.", message))

            return
        } else {
            s.ChannelMessageSend(m.ChannelID,
                                 "You can only use this command in the " +
                                 "#stats channel!")

        }

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

        height, unix, err := getPoolHeightAndTimestamp(v.api)

        if err == nil {
            v.height = height
            v.timeLastFound = time.Unix(unix, 0)
        } else {
            v.height = 0
        }
    }
}

func getPoolHeightAndTimestamp (apiURL string) (int, int64, error) {
    statsURL := apiURL + "stats"

    http.DefaultTransport.(*http.Transport).TLSClientConfig = 
        &tls.Config{InsecureSkipVerify: true}

    resp, err := http.Get(statsURL)

    if err != nil {
        fmt.Printf("Failed to download stats from %s! Error: %s\n", 
                    statsURL, err)
        return 0, 0, err
    }

    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)

    if err != nil {
        fmt.Printf("Failed to download stats from %s! Error: %s\n",
                    statsURL, err)
        return 0, 0, err
    }

    /* Some servers (Looking at you us.turtlepool.space! send us deflate'd
       content even when we didn't ask for it - uncompress it */
    if resp.Header.Get("Content-Encoding") == "deflate" {
        body, err = ioutil.ReadAll(flate.NewReader(bytes.NewReader(body)))

        if err != nil {
            fmt.Println("Failed to deflate response from", statsURL)
            return 0, 0, err
        }
    }

    heightRegex := regexp.MustCompile(".*\"height\":(\\d+).*")
    blockFoundRegex := regexp.MustCompile(".*\"lastBlockFound\":\"(\\d+)\".*")

    height := heightRegex.FindStringSubmatch(string(body))
    blockFound := blockFoundRegex.FindStringSubmatch(string(body))

    if len(height) < 2 {
        fmt.Println("Failed to parse height from", statsURL)
        return 0, 0, errors.New("Couldn't parse height")
    }

    if len(blockFound) < 2 {
        fmt.Println("Failed to parse block last found timestamp from", statsURL)
        return 0, 0, errors.New("Couldn't parse block timestamp")
    }

    i, err := strconv.Atoi(height[1])

    if err != nil {
        fmt.Println("Failed to convert height into int! Error:", err)
        return 0, 0, err
    }

    str := blockFound[1]
    blockFound[1] = str[0:len(str) - 3]
    
    /* Don't overflow on 32 bit */
    unix, err := strconv.ParseInt(blockFound[1], 10, 64)

    if err != nil {
        fmt.Println("Failed to convert timestamp into int! Error:", err)
        return 0, 0, err
    }

    return i, unix, nil
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

    discord.StateEnabled = true

    fmt.Println("Connected to discord!")

    return discord, nil
}

func getToken() (string, error) {
    file, err := os.Open("token.txt")

    if err != nil {
        return "", err
    }

    defer file.Close()

    reader := bufio.NewReader(file)

    line, err := reader.ReadString('\n')

    if err != nil {
        return "", err
    }

    line = strings.TrimSuffix(line, "\n")

    return line, nil
}
