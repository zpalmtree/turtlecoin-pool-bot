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
)

const poolsJSON string = "https://raw.githubusercontent.com/turtlecoin/" +
                         "turtlecoin-pools-json/master/turtlecoin-pools.json"

/* You will need to change this to the channel ID of the pools channel. To
   get this, go here - https://stackoverflow.com/a/41515544/8737306 */
const poolsChannel string = "426881205263269900"

/* The amount of blocks a pool can vary from the others before we notify */
const poolMaxDifference int = 0

/* How often we check the pools */
const poolRefreshRate time.Duration = time.Second * 30

type Pool struct {
    Url string `json:??,string`
    Api string `json:url,string`
}

type Pools map[string]*Pool

var globalPools Pools
var globalHeights map[string]int
var globalHeight int
var globalClaims map[string]string

func main() {
    /* Need to not shadow global variables */
    var err error

    globalPools, err = getPools()

    if err != nil {
        return
    }

    globalHeights = getHeights(globalPools)

    globalHeight = median(getValues(globalHeights))

    globalClaims, err = getClaims()

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

func writeClaims() {
    file, err := os.Create("claims.txt")

    if err != nil {
        fmt.Println("Failed to open file! Error:", err)
        return
    }

    defer file.Close()

    for k, v := range globalClaims {
        file.WriteString(fmt.Sprintf("%s:%s\n", k, v))
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

func heightWatcher(s *discordgo.Session) {
    for {
        time.Sleep(poolRefreshRate)

        globalHeights = getHeights(globalPools)
        globalHeight = median(getValues(globalHeights))

        sendMessage := false

        poolOwners := make([]string, 0)

        msg := fmt.Sprintf("```It looks like some pools are stuck, forked, " +
                           "or behind!\nMedian pool height: %d\n\n", 
                           globalHeight)

        for k, v := range globalHeights {
            if v > globalHeight + poolMaxDifference || 
               v < globalHeight - poolMaxDifference {
                msg += fmt.Sprintf("%-25s %d\n", k, v)

                if val, ok := globalClaims[k]; ok {
                    /* Add the pool owner ID to mention later */
                    poolOwners = append(poolOwners, val)
                }

                sendMessage = true
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
}

/* Update the pools json every hour */
func poolUpdater() {
    for {
        time.Sleep(time.Hour)

        tmpPools, err := getPools()
        
        if err != nil {
            return
        }

        globalPools = tmpPools
    }
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
    /* Ignore our own messages */
    if m.Author.ID == s.State.User.ID {
        return
    }

    if m.Content == ".heights" {
        heightsPretty := "```\nAll known pool heights:\n\n"

        for k, v := range globalHeights {
            heightsPretty += fmt.Sprintf("%-25s %d\n", k, v)
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
                   ".claim <pool>   Claim the pool <pool> as your pool```")


        s.ChannelMessageSend(m.ChannelID, helpCommand)

        return
    }

    if m.Content == ".height" {
        s.ChannelMessageSend(m.ChannelID, 
                             fmt.Sprintf("```Median pool height:\n\n%d```", 
                                         globalHeight))

        return
    }

    if strings.HasPrefix(m.Content, ".height") {
        message := strings.TrimPrefix(m.Content, ".height")
        /* Remove first char - probably a space but should make sure */
        message = message[1:]

        for k, v := range globalHeights {
            if k == message {
                s.ChannelMessageSend(m.ChannelID,
                                     fmt.Sprintf("```%s pool height:\n\n%d```",
                                                 k, v))
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

        for k, _ := range globalPools {
            if k == message {
                /* Pool has already been claimed */
                if val, ok := globalClaims[k]; ok {
                    user, err := s.User(val)

                    if err != nil {
                        fmt.Println("Couldn't find user! Error:", err)
                    }

                    s.ChannelMessageSend(m.ChannelID,
                                         fmt.Sprintf("%s has already been " +
                                                     "claimed by %s!", k,
                                                     user.Username))
                    
                    return
                /* Otherwise insert into the map */
                } else {
                    globalClaims[k] = m.Author.ID
                    
                    s.ChannelMessageSend(m.ChannelID,
                                         fmt.Sprintf("You have claimed %s!", k))

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

func median(heights []int) int {
    sort.Ints(heights)

    half := len(heights) / 2
    median := heights[half]

    if len(heights) % 2 == 0 {
        median = (median + heights[half-1]) / 2
    }

    return median
}

func getHeights (pools Pools) map[string]int {
    heights := make(map[string]int)

    for _, v := range pools {
        height, err := getPoolHeight(v.Api)

        if err == nil {
            heights[v.Url] = height
        }
    }

    return heights
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

/* Thanks to https://stackoverflow.com/a/48716447/8737306 */
func (p *Pools) UnmarshalJSON (data []byte) error {
    var transient = make(map[string]*Pool)

    err := json.Unmarshal(data, &transient)

    if err != nil {
        return err
    }

    /* Not sure why this is parsing kinda backwards... */
    for k, v := range transient {
        v.Api = v.Url
        v.Url = k
        (*p)[k] = v
    }

    fmt.Println("Got pools json!")

    return nil
}

func getPools() (Pools, error) {
    var pools Pools = make(map[string]*Pool)

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

    err = pools.UnmarshalJSON(body)

    if err != nil {
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
