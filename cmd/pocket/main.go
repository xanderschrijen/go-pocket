package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"text/template"

	"github.com/docopt/docopt-go"
	"github.com/xanderschrijen/go-pocket/api"
	"github.com/xanderschrijen/go-pocket/auth"
)

var version = "0.1"

var defaultItemTemplate = template.Must(template.New("item").Parse(
	`[{{.ItemID | printf "%9d"}}] {{.Title}} <{{.URL}}>`,
))

var configDir string

func init() {
	usr, err := user.Current()
	if err != nil {
		panic(err)
	}

	configDir = filepath.Join(usr.HomeDir, ".config", "pocket")
	err = os.MkdirAll(configDir, 0777)
	if err != nil {
		panic(err)
	}
}

func main() {
	usage := `A Pocket <getpocket.com> client.

Usage:
  pocket list [--format=<template>] [--domain=<domain>] [--tag=<tag>] [--search=<query>]
  pocket archive <item-id>
  pocket add <url> [--title=<title>] [--tags=<tags>]

Options for list:
  -f, --format <template> A Go template to show items.
  -d, --domain <domain>   Filter items by its domain when listing.
  -s, --search <query>    Search query when listing.
  -t, --tag <tag>         Filter items by a tag when listing.

Options for add:
  --title <title>         A manually specified title for the article
  --tags <tags>           A comma-separated list of tags
`

	arguments, err := docopt.ParseArgs(usage, nil, version)
	if err != nil {
		panic(err)
	}

	consumerKey := getConsumerKey()

	accessToken, err := restoreAccessToken(consumerKey)
	if err != nil {
		panic(err)
	}

	client := api.NewClient(consumerKey, accessToken.AccessToken)

	if do, ok := arguments["list"].(bool); ok && do {
		commandList(arguments, client)
	} else if do, ok := arguments["archive"].(bool); ok && do {
		commandArchive(arguments, client)
	} else if do, ok := arguments["add"].(bool); ok && do {
		commandAdd(arguments, client)
	} else {
		panic("Not implemented")
	}
}

type bySortID []api.Item

func (s bySortID) Len() int           { return len(s) }
func (s bySortID) Less(i, j int) bool { return s[i].SortId < s[j].SortId }
func (s bySortID) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func commandList(arguments map[string]interface{}, client *api.Client) {
	options := &api.RetrieveOption{}

	if domain, ok := arguments["--domain"].(string); ok {
		options.Domain = domain
	}

	if search, ok := arguments["--search"].(string); ok {
		options.Search = search
	}

	if tag, ok := arguments["--tag"].(string); ok {
		options.Tag = tag
	}

	res, err := client.Retrieve(options)
	if err != nil {
		panic(err)
	}

	var itemTemplate *template.Template
	if format, ok := arguments["--format"].(string); ok {
		itemTemplate = template.Must(template.New("item").Parse(format))
	} else {
		itemTemplate = defaultItemTemplate
	}

	items := []api.Item{}
	for _, item := range res.List {
		items = append(items, item)
	}

	sort.Sort(bySortID(items))

	for _, item := range items {
		err := itemTemplate.Execute(os.Stdout, item)
		if err != nil {
			panic(err)
		}
		fmt.Println("")
	}
}

func commandArchive(arguments map[string]interface{}, client *api.Client) {
	if itemIDString, ok := arguments["<item-id>"].(string); ok {
		itemID, err := strconv.Atoi(itemIDString)
		if err != nil {
			panic(err)
		}

		action := api.NewArchiveAction(itemID)
		res, err := client.Modify(action)
		fmt.Println(res, err)
	} else {
		panic("Wrong arguments")
	}
}

func commandAdd(arguments map[string]interface{}, client *api.Client) {
	options := &api.AddOption{}

	url, ok := arguments["<url>"].(string)
	if !ok {
		panic("Wrong arguments")
	}

	options.URL = url

	if title, ok := arguments["--title"].(string); ok {
		options.Title = title
	}

	if tags, ok := arguments["--tags"].(string); ok {
		options.Tags = tags
	}

	err := client.Add(options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func getConsumerKey() string {
	consumerKeyPath := filepath.Join(configDir, "consumer_key")
	consumerKey, err := ioutil.ReadFile(consumerKeyPath)

	if err != nil {
		log.Printf("Can't get consumer key: %v", err)
		log.Print("Enter your consumer key (from here https://getpocket.com/developer/apps/): ")

		consumerKey, _, err = bufio.NewReader(os.Stdin).ReadLine()
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(consumerKeyPath, consumerKey, 0600)
		if err != nil {
			panic(err)
		}

		return string(consumerKey)
	}

	return string(bytes.SplitN(consumerKey, []byte("\n"), 2)[0])
}

func restoreAccessToken(consumerKey string) (*auth.Authorization, error) {
	accessToken := &auth.Authorization{}
	authFile := filepath.Join(configDir, "auth.json")

	err := loadJSONFromFile(authFile, accessToken)

	if err != nil {
		log.Println(err)

		accessToken, err = obtainAccessToken(consumerKey)
		if err != nil {
			return nil, err
		}

		err = saveJSONToFile(authFile, accessToken)
		if err != nil {
			return nil, err
		}
	}

	return accessToken, nil
}

func obtainAccessToken(consumerKey string) (*auth.Authorization, error) {
	ch := make(chan struct{})
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/favicon.ico" {
				http.Error(w, "Not Found", 404)
				return
			}

			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintln(w, "Authorized.")
			ch <- struct{}{}
		}))
	defer ts.Close()

	redirectURL := ts.URL

	requestToken, err := auth.ObtainRequestToken(consumerKey, redirectURL)
	if err != nil {
		return nil, err
	}

	url := auth.GenerateAuthorizationURL(requestToken, redirectURL)
	fmt.Println(url)

	<-ch

	return auth.ObtainAccessToken(consumerKey, requestToken)
}

func saveJSONToFile(path string, v interface{}) error {
	w, err := os.Create(path)
	if err != nil {
		return err
	}

	defer w.Close()

	return json.NewEncoder(w).Encode(v)
}

func loadJSONFromFile(path string, v interface{}) error {
	r, err := os.Open(path)
	if err != nil {
		return err
	}

	defer r.Close()

	return json.NewDecoder(r).Decode(v)
}
