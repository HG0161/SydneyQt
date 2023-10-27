package main

import (
	"context"
	"fmt"
	"github.com/pkoukk/tiktoken-go"
	"github.com/tidwall/gjson"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"log"
	"strings"
)

const (
	_ = iota
	AskTypeSydney
	AskTypeOpenAI
)

type AskType int

// App struct
type App struct {
	debug    bool
	settings *Settings
	ctx      context.Context
}

// NewApp creates a new App application struct
func NewApp(settings *Settings) *App {
	return &App{debug: false, settings: settings}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

type AskOptions struct {
	Type        AskType `json:"type"`
	ChatContext string  `json:"chatContext"`
	Prompt      string  `json:"prompt"`
	ImageURL    string  `json:"imageURL"`
	ReplyDeep   int     `json:"reply_deep"`
}

const (
	EventChatAlert              = "chat_alert"
	EventChatAppend             = "chat_append"
	EventChatFinish             = "chat_finish"
	EventChatSuggestedResponses = "chat_suggested_responses"
	EventChatToken              = "chat_token"
)

const (
	EventChatStop = "chat_stop"
)

func (a *App) askSydney(options AskOptions) {
	sydney := NewSydney(a.debug, ReadCookiesFile(), a.settings.config.Proxy,
		a.settings.config.ConversationStyle, a.settings.config.Locale, "wss://"+a.settings.config.WssDomain,
		a.settings.config.NoSearch)
	conversation, err := sydney.CreateConversation()
	if err != nil {
		runtime.EventsEmit(a.ctx, EventChatAlert, err.Error())
		return
	}
	stopCtx, cancel := CreateCancelContext()
	defer cancel()
	go func() {
		runtime.EventsOn(a.ctx, EventChatStop, func(optionalData ...interface{}) {
			cancel()
		})
	}()
	ch := sydney.AskStream(stopCtx, conversation, options.Prompt, options.ChatContext, options.ImageURL)
	defer runtime.EventsEmit(a.ctx, EventChatFinish)
	sendSuggResp := func(message gjson.Result) {
		if message.Get("suggestedResponses").Exists() {
			runtime.EventsEmit(a.ctx, EventChatSuggestedResponses,
				Map(message.Get("suggestedResponses").Array(), func(v gjson.Result) string {
					return v.Get("text").String()
				}),
			)
		}
	}
	chatAppend := func(text string) {
		runtime.EventsEmit(a.ctx, EventChatAppend, text)
	}
	tk, err := tiktoken.EncodingForModel("gpt-4")
	if err != nil {
		panic(err)
	}
	messageRevoked := false
	wrote := 0
	replied := false
	for msg := range ch {
		if msg.Error != nil {
			runtime.EventsEmit(a.ctx, EventChatAlert, err.Error())
			return
		}
		data := gjson.Parse(msg.Data)
		if data.Get("type").Int() == 1 && data.Get("arguments.0.messages").Exists() {
			message := data.Get("arguments.0.messages.0")
			msgType := message.Get("messageType")
			messageText := message.Get("text").String()
			messageHiddenText := message.Get("hiddenText").String()
			switch msgType.String() {
			case "InternalSearchQuery":
				chatAppend("[assistant](#search_query)\n" + messageHiddenText + "\n\n")
			case "InternalSearchResult":
				var links []string
				if strings.Contains(messageHiddenText,
					"Web search returned no relevant result") {
					chatAppend("[assistant](#search_query)\n" + messageHiddenText + "\n\n")
					continue
				}
				if !gjson.Valid(messageText) {
					log.Println("Error when parsing InternalSearchResult: " + messageText)
					continue
				}
				arr := gjson.Parse(messageText).Array()
				for _, group := range arr {
					srIndex := 1
					for _, subGroup := range group.Array() {
						links = append(links, fmt.Sprintf("[^%d^][%s](%s)",
							srIndex, subGroup.Get("title").String(), subGroup.Get("url").String()))
						srIndex++
					}
				}
				chatAppend("[assistant](#search_results)\n" + strings.Join(links, "\n\n") + "\n\n")
			case "InternalLoaderMessage":
				if message.Get("hiddenText").Exists() {
					chatAppend("[assistant](#loading)\n" + messageHiddenText + "\n\n")
					continue
				}
				if message.Get("text").Exists() {
					chatAppend("[assistant](#loading)\n" + messageText + "\n\n")
					continue
				}
				chatAppend("[assistant](#loading)\n" + message.Raw + "\n\n")
			case "GenerateContentQuery":
				if message.Get("contentType").String() != "IMAGE" {
					continue
				}
				chatAppend("[assistant](#generative_image)\nKeyword: " +
					messageText + "\n\n")
			case "":
				if data.Get("arguments.0.cursor").Exists() {
					chatAppend("[assistant](#message)\n")
					wrote = 0
				}
				if message.Get("contentOrigin").String() == "Apology" {
					messageRevoked = true
					if replied &&
						(a.settings.config.RevokeReplyText == "" || options.ReplyDeep >= a.settings.config.RevokeReplyCount) {
						runtime.EventsEmit(a.ctx, EventChatAlert, "Message revoke detected")
					} else {
						runtime.EventsEmit(a.ctx, EventChatAlert,
							"Looks like the user's message has triggered the Bing filter")
					}
					return
				} else {
					replied = true
					chatAppend(messageText[wrote:])
					wrote = len(messageText)
					runtime.EventsEmit(a.ctx, EventChatToken,
						len(tk.Encode(messageText, nil, nil)))
					sendSuggResp(message)
				}
			default:
				log.Println("Unsupported message type: " + msgType.String())
				log.Println("Triggered by " + options.Prompt + ", response: " + message.Raw)
			}
		} else if data.Get("type").Int() == 2 && data.Get("item.messages").Exists() {
			message := data.Get("item.messages|@reverse|0")
			sendSuggResp(message)
		}
	}
}
func (a *App) AskAI(options AskOptions) {
	if options.Type == AskTypeSydney {
		a.askSydney(options)
	} else if options.Type == AskTypeOpenAI {
		runtime.EventsEmit(a.ctx, EventChatAlert, "not implemented")
	}
}
