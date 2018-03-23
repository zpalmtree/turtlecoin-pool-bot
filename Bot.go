package main

import (
    "github.com/bwmarrin/discordgo"
    "fmt"
    "os"
    "bufio"
    "strings"
    "net/http"
    "encoding/json"
    "io/ioutil"
)

const poolsJSON string = "https://raw.githubusercontent.com/turtlecoin/" +
                         "turtlecoin-pools-json/master/turtlecoin-pools.json"

type Pool struct {
    Url string `json:??,string`
    Api string `json:url,string`
}

type Pools map[string]*Pool

func main() {
    _, err := startup()
    
    if err != nil {
        return
    }

    pools, err := getPools()

    if err != nil {
        return
    }
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

    discord, err = discordgo.New(string(token))

    if err != nil {
        fmt.Println("Failed to init bot! Error:", err)
        return discord, err
    }

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
