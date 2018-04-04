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
    "time"
    "compress/flate"
    "bytes"
    "crypto/tls"
)

const poolsJSON string = "https://raw.githubusercontent.com/turtlecoin/" +
                         "turtlecoin-pools-json/master/turtlecoin-pools.json"

/* You will need to change this to the channel ID of the pools channel. To
   get this, go here - https://stackoverflow.com/a/41515544/8737306 */
//const poolsChannel string = "430779541921726465"

/* test channel */
const poolsChannel string = "426881205263269900"

/* The amount of blocks a pool can vary from the others before we notify */
const poolMaxDifference int = 5

/* How often we check the pools */
const poolRefreshRate time.Duration = time.Second * 30

/* We ignore some pools from the forked/api down message because they are
   constantly up and down and are quite noisy */
var ignoredPools = []string { /* "turtle.coolmining.club" */ }

/* The data type we parse our json into */
type Pool struct {
    Api string `json:"url"`
}

/* Map of pool name to pool api */
type Pools map[string]Pool

/* Info about every pool */
type PoolsInfo struct {
    pools               []PoolInfo
    modeHeight          int
    heightLastUpdated   time.Time
    warned              bool
}

/* Info about an individual pool */
type PoolInfo struct {
    url                 string
    api                 string
    claimees            []string
    height              int
    apiFailCounter      int
    warnedApi           bool
    warnedHeight        bool
    pinged              bool
    recovered           bool
    timeLastFound       time.Time
    timeStuck           time.Time
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
            p.claimees = val
        }

        p.apiFailCounter = 0

        p.warnedApi = false
        p.warnedHeight = false
        p.pinged = false
        p.recovered = false

        poolInfo = append(poolInfo, p)
    }

    /* Update the global struct */
    globalInfo.pools = poolInfo
    globalInfo.warned = false

    populateHeights()
    updateModeHeight()

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
        for _, owner := range v.claimees {
            file.WriteString(fmt.Sprintf("%s:%s\n", v.url, owner))
        }
    }

    file.Sync()
}

func getClaims() (map[string][]string, error) {
    claims := make(map[string][]string)

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

            /* Pool doesn't exist yet, create slice */
            if _, ok := claims[matches[1]]; !ok {
                claims[matches[1]] = make([]string, 0)
            }

            claims[matches[1]] = append(claims[matches[1]], matches[2])
        }

        if err := scanner.Err(); err != nil {
            fmt.Println("Failed to read claims.txt! Error:", err)
            return claims, err
        }
    }

    return claims, nil
}

func elem(needle string, haystack []string) bool {
    for _, v := range haystack {
        if v == needle {
            return true
        }
    }

    return false
}

func printStatus(s *discordgo.Session) {
    pingees := make([]string, 0)

    msg := fmt.Sprintf("```Median pool height: %d\n" +
                       "Block Last Found: %s ago\n\n" +
                       "Currently Downed Pools     Height     " +
                       "Status     Block Last Found     Time Stuck\n\n",
                       globalInfo.modeHeight,
                       formatTime(globalInfo.heightLastUpdated))

    for index, _ := range globalInfo.pools {
        v := &globalInfo.pools[index]

        status := ""

        if v.height == 0 {
            status = "Api down"
        } else if v.height > globalInfo.modeHeight + poolMaxDifference ||
           v.height < globalInfo.modeHeight - poolMaxDifference {
            status = "Forked"
        } else if v.recovered {
            status = "Recovered"
        } else {
            continue
        }

        name := v.url

        /* Bold the pools that caused the list to change */
        if v.recovered || !v.pinged {
            name = fmt.Sprintf("*%s", v.url)
        }

        v.recovered = false

        msg += fmt.Sprintf("%-26s %-11d%-11s%-21s%s\n", name, v.height, 
                           status, formatTime(v.timeLastFound) + " ago",
                           formatTime(v.timeStuck))

        for _, owner := range v.claimees {
            /* Don't ping user more than once if they have multiple claims
               Only ping them the first time their pool goes down */
            if !v.pinged && !elem(owner, pingees) {
                pingees = append(pingees, owner)
            }
        }

        v.pinged = true
    }

    msg += "```"

    for _, owner := range pingees {
        /* Ping the owners */
        msg += fmt.Sprintf("<@%s> ", owner)
    }

    s.ChannelMessageSend(poolsChannel, msg)

    return
}

func checkForApiIssues(v *PoolInfo) bool {
    if v.height == 0 {
        /* Maybe their api momentarily went down or something, don't
           instantly ping */
        if v.apiFailCounter <= 2 {
            v.apiFailCounter++
        /* Only warn the user once */
        } else if !v.warnedApi {
            v.warnedApi = true
            v.pinged = false
            v.timeStuck = time.Now()
            return true
        }
    } else {
        v.apiFailCounter = 0

        /* Recovered, reprint message */
        if v.warnedApi {
            v.warnedApi = false
            v.recovered = true
            return true
        }
    }

    return false
}

func checkForHeightIssues(v *PoolInfo) bool {
    if v.height == 0 {
        return false
    } else if v.height > globalInfo.modeHeight + poolMaxDifference ||
              v.height < globalInfo.modeHeight - poolMaxDifference {
        if !v.warnedHeight {
            v.warnedHeight = true
            v.pinged = false
            v.timeStuck = time.Now()
            return true
        }
    } else {
        /* Recovered, reprint message */
        if v.warnedHeight {
            v.warnedHeight = false
            v.recovered = true
            return true
        }
    }

    return false
}


func checkForPoolsWithIssues(s *discordgo.Session) {
    newIssues := false

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

        /* If pool has new issue, or old issue recovered, print out pool 
           status. Note that we CAN'T break out of the loop yet - the checks
           update the warned boolean, which we need to make sure we only
           reprint the update when something changes */
        if checkForApiIssues(v) || checkForHeightIssues(v) {
            newIssues = true
        }
    }

    if newIssues {
        printStatus(s)
    }
}

func checkForStuckChain(s *discordgo.Session) {
    timeSinceLastBlock := time.Since(globalInfo.heightLastUpdated)

    /* Alert if the chain has been stuck for longer than 5 minutes */
    if timeSinceLastBlock > (time.Minute * 5) {
        /* Only warn once */
        if !globalInfo.warned {
            s.ChannelMessageSend(poolsChannel,
                                 fmt.Sprintf("```It looks like the chain is " +
                                             "stuck! The last block was " +
                                             "found %d minutes ago!```", 
                                             int(timeSinceLastBlock.Minutes())))
            globalInfo.warned = true
        }
    /* We have already warned, so print out a recovery message */
    } else if globalInfo.warned {
        globalInfo.warned = false
        s.ChannelMessageSend(poolsChannel,
                             fmt.Sprintf("```The chain appears to have " +
                                         "recovered. The last block was " +
                                         "found %d minutes ago.```",
                                         int(timeSinceLastBlock.Minutes())))
    }
}

func heightWatcher(s *discordgo.Session) {
    for {
        time.Sleep(poolRefreshRate)

        populateHeights()
        updateModeHeight()

        checkForStuckChain(s)
        checkForPoolsWithIssues(s)
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
                p.claimees = val
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
                    p.pinged = localPool.pinged
                    p.recovered = localPool.recovered
                    p.height = localPool.height
                    p.timeLastFound = localPool.timeLastFound
                    p.timeStuck = localPool.timeStuck
                    break
                }
            }

            poolInfo = append(poolInfo, p)
        }

        /* Update the global struct */
        globalInfo.pools = poolInfo
        populateHeights()
        updateModeHeight()
    }
}

func formatTime(when time.Time) string {
    mins := int(time.Since(when).Minutes())
    hours := int(time.Since(when).Hours())

    if when.IsZero() {
        return "Never"
    } else if mins < 60 {
        return fmt.Sprintf("%d minutes", mins);
    } else if hours < 24 {
        return fmt.Sprintf("%d hours", hours);
    } else {
        return fmt.Sprintf("%d hours", int(hours / 24));
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

    if m.Content == "/heights" || m.Content == "/status" {
        heightsPretty := fmt.Sprintf("```Median pool height: %d\n" +
                                     "Block Last Found: %s ago\n\n" +
                                     "Pool                      Height     " +
                                     "Status     Block Last Found\n\n",
                                     globalInfo.modeHeight,
                                     formatTime(globalInfo.heightLastUpdated))

        for _, v := range globalInfo.pools {
            status := "Ok"

            if v.height == 0 {
                status = "Api down"
            } else if v.height > globalInfo.modeHeight + poolMaxDifference ||
                      v.height < globalInfo.modeHeight - poolMaxDifference {
                status = "Forked"
            }

            heightsPretty += fmt.Sprintf("%-25s %-11d%-11s%s ago\n", v.url,
                                         v.height, status,
                                         formatTime(v.timeLastFound))
        }

        heightsPretty += "```"

        s.ChannelMessageSend(m.ChannelID, heightsPretty)

        return
    }

    if m.Content == "/help" {
        helpCommand := fmt.Sprintf("```\nAvailable commands:\n\n" +
                   "/help           Display this help message\n" +
                   "/heights        Display the heights of all known pools\n" +
                   "/status         An alias for /heights\n" +
                   "/height         Display the median height of all pools\n" +
                   "/height <pool>  Display the height of <pool>\n" +
                   "/watch <pool>   Watch the pool <pool> so you can be " +
                                   "sent notifications```")

        s.ChannelMessageSend(m.ChannelID, helpCommand)

        return
    }

    if m.Content == "/height" {
        s.ChannelMessageSend(m.ChannelID, 
                             fmt.Sprintf("```Median pool height: %d```", 
                                         globalInfo.modeHeight))

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

    if m.Content == "/watch" {
        if m.ChannelID == poolsChannel {
            s.ChannelMessageSend(m.ChannelID,
                                 "You must specify a pool to watch!\nType " +
                                 "`/heights` to list all pools.")

        } else {
            s.ChannelMessageSend(m.ChannelID,
                                 "You can only use this command in the " +
                                 "#stats channel!")
        }

        return
    }

    if strings.HasPrefix(m.Content, "/watch") {
        if m.ChannelID == poolsChannel {
            message := strings.TrimPrefix(m.Content, "/watch")
            message = message[1:]

            for index, _ := range globalInfo.pools {
                v := &globalInfo.pools[index]

                if v.url == message {
                    if elem(m.Author.ID, v.claimees) {
                        s.ChannelMessageSend(m.ChannelID,
                                             fmt.Sprintf("You are already " +
                                                         "watching %s!",
                                                         v.url))
                        return
                    }

                    v.claimees = append(v.claimees, m.Author.ID)
                    
                    s.ChannelMessageSend(m.ChannelID,
                                         fmt.Sprintf("You are watching %s!", 
                                                     v.url))

                    writeClaims()

                    return
                }
            }

            s.ChannelMessageSend(m.ChannelID,
                                 fmt.Sprintf("Could find pool %s - type " +
                                             "`/heights` to view all known " +
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

func updateModeHeight() {
    heights := make([]int, 0)

    for _, v := range globalInfo.pools {
        heights = append(heights, v.height)
    }

    mode := mode(heights)

    if mode != globalInfo.modeHeight {
        globalInfo.modeHeight = mode
        globalInfo.heightLastUpdated = time.Now()
    }
}

/* Ok, this is actually calculating the mode. Mode pool height just sounds
   weird */
func mode(a []int) int {
    m := make(map[int]int)
    for _, v := range a {
        m[v]++
    }
    var mode []int
    var n int
    for k, v := range m {
        switch {
        case v < n:
        case v > n:
            n = v
            mode = append(mode[:0], k)
        default:
            mode = append(mode, k)
        }
    }

    return mode[0]
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

    /* Some servers (Looking at you us.turtlepool.space!) send us deflate'd
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
