/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"sigs.k8s.io/slack-infra/slack"
	"sigs.k8s.io/slack-infra/slack-moderator-words/model"
)

type handler struct {
	client  *slack.Client
	filters model.FilterConfig
}

// ServeHTTP handles Slack webhook requests.
func (h *handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logError(rw, "Failed to read incoming request body: %v", err)
		return
	}
	defer r.Body.Close()

	if err := h.client.VerifySignature(body, r.Header); err != nil {
		logError(rw, "Failed validation: %v", err)
		return
	}

	event := &model.SlackEvent{}
	err = json.NewDecoder(bytes.NewReader(body)).Decode(event)
	if err != nil {
		logError(rw, "Failed to unmarshal payload: %v", err)
		panic(err)
	}

	// This is used for the first time when configuring the slack events
	if event.Type == "url_verification" {
		resp := &model.Challenge{}
		resp.Challenge = event.Challenge
		challengeJson, err := json.Marshal(resp)
		if err != nil {
			logError(rw, "Failed to marshal challenge payload: %v", err)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write(challengeJson)
		return
	}

	// Triggered when is a new channel created
	// and the bot will join to the channel
	// Slack Event needed for this: channel_created
	if event.Event.Type == "channel_created" {
		b, err := json.Marshal(event.Event.Channel)
		if err != nil {
			panic(err)
		}
		channelCreated := &model.Channel{}
		err = json.Unmarshal(b, channelCreated)
		if err != nil {
			log.Fatalf("Failed to decode event channel: %v", err)
		}

		log.Printf("New public channels: %s/%s\n", channelCreated.ID, channelCreated.Name)
		req := map[string]interface{}{
			"channel": channelCreated.ID,
		}
		err = h.client.CallMethod("conversations.join", req, nil)
		if err != nil {
			log.Fatalf("Failed to join channel %s: %v", channelCreated.Name, err)
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(""))
		return
	}

	// When is a message from the channels the bot is listening
	// Slack Event needed for this: message.channels
	// reply ok rigth away
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(""))

	// If come from Bot just ignore and not moderate
	if event.Event.BotID != "" {
		return
	}

	log.Printf("[EVENT] %+v", event)

	if h.filters != nil {
		for _, filter := range h.filters {
			for _, word := range filter.Triggers {
				matched, err := regexp.MatchString(word, event.Event.Text)
				if err != nil {
					logError(rw, "Failed to match string for trigger %s: %v", word, err)
					continue
				}
				if matched {
					req := map[string]interface{}{
						"channel": event.Event.Channel,
						"user":    event.Event.User,
						"text":    filter.Message,
					}

					if event.Event.ThreadTS != "" {
						req["thread_ts"] = event.Event.ThreadTS
					}

					err = h.client.CallMethod(filter.Action, req, nil)
					if err != nil {
						logError(rw, "Failed send message to slack: %v", err)
					}
				}
			}
		}
	}
}

func logError(rw http.ResponseWriter, format string, args ...interface{}) {
	s := fmt.Sprintf(format, args...)
	log.Println(s)
	http.Error(rw, s, 500)
}
