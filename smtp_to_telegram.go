package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	units "github.com/docker/go-units"
	"github.com/flashmob/go-guerrilla"
	"github.com/flashmob/go-guerrilla/backends"
	"github.com/flashmob/go-guerrilla/log"
	"github.com/flashmob/go-guerrilla/mail"
	"github.com/jhillyerd/enmime"
	"github.com/urfave/cli/v2"
)

var (
	Version string = "UNKNOWN_RELEASE"
	logger  log.Logger
)

const (
	BodyTruncated = "\n\n[truncated]"
)

type SmtpConfig struct {
	smtpListen          string
	smtpPrimaryHost     string
	smtpMaxEnvelopeSize int64
}

type TelegramConfig struct {
	telegramChatIds                  string
	telegramBotToken                 string
	telegramApiPrefix                string
	telegramApiTimeoutSeconds        float64
	messageTemplate                  string
	forwardedAttachmentMaxSize       int
	forwardedAttachmentMaxPhotoSize  int
	forwardedAttachmentRespectErrors bool
	messageLengthToSendAsFile        uint
}

type TelegramAPIMessageResult struct {
	Ok     bool                `json:"ok"`
	Result *TelegramAPIMessage `json:"result"`
}

type TelegramAPIMessage struct {
	// https://core.telegram.org/bots/api#message
	MessageId json.Number `json:"message_id"`
}

type FormattedEmail struct {
	text        string
	attachments []*FormattedAttachment
}

const (
	ATTACHMENT_TYPE_DOCUMENT = iota
	ATTACHMENT_TYPE_PHOTO    = iota
)

type FormattedAttachment struct {
	filename string
	caption  string
	content  []byte
	fileType int
}

func GetHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		panic(fmt.Sprintf("Unable to detect hostname: %s", err))
	}
	return hostname
}

func main() {
	app := cli.NewApp()
	app.Name = "smtp_to_telegram"
	app.Usage = "A small program which listens for SMTP and sends " +
		"all incoming Email messages to Telegram."
	app.Version = Version
	app.Action = func(c *cli.Context) error {
		smtpMaxEnvelopeSize, err := units.FromHumanSize(c.String("smtp-max-envelope-size"))
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		smtpConfig := &SmtpConfig{
			smtpListen:          c.String("smtp-listen"),
			smtpPrimaryHost:     c.String("smtp-primary-host"),
			smtpMaxEnvelopeSize: smtpMaxEnvelopeSize,
		}
		forwardedAttachmentMaxSize, err := units.FromHumanSize(c.String("forwarded-attachment-max-size"))
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		forwardedAttachmentMaxPhotoSize, err := units.FromHumanSize(c.String("forwarded-attachment-max-photo-size"))
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		telegramConfig := &TelegramConfig{
			telegramChatIds:                  c.String("telegram-chat-ids"),
			telegramBotToken:                 c.String("telegram-bot-token"),
			telegramApiPrefix:                c.String("telegram-api-prefix"),
			telegramApiTimeoutSeconds:        c.Float64("telegram-api-timeout-seconds"),
			messageTemplate:                  c.String("message-template"),
			forwardedAttachmentMaxSize:       int(forwardedAttachmentMaxSize),
			forwardedAttachmentMaxPhotoSize:  int(forwardedAttachmentMaxPhotoSize),
			forwardedAttachmentRespectErrors: c.Bool("forwarded-attachment-respect-errors"),
			messageLengthToSendAsFile:        c.Uint("message-length-to-send-as-file"),
		}
		d, err := SmtpStart(smtpConfig, telegramConfig)
		if err != nil {
			panic(fmt.Sprintf("start error: %s", err))
		}
		sigHandler(d)
		return nil
	}
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "smtp-listen",
			Value:   "127.0.0.1:2525",
			Usage:   "SMTP: TCP address to listen to",
			EnvVars: []string{"ST_SMTP_LISTEN"},
		},
		&cli.StringFlag{
			Name:    "smtp-primary-host",
			Value:   GetHostname(),
			Usage:   "SMTP: primary host",
			EnvVars: []string{"ST_SMTP_PRIMARY_HOST"},
		},
		&cli.StringFlag{
			Name:    "smtp-max-envelope-size",
			Usage:   "Max size of an incoming Email. Examples: 5k, 10m.",
			Value:   "50m",
			EnvVars: []string{"ST_SMTP_MAX_ENVELOPE_SIZE"},
		},
		&cli.StringFlag{
			Name:     "telegram-chat-ids",
			Usage:    "Telegram: comma-separated list of chat ids, could also have email mapping to chat id",
			EnvVars:  []string{"ST_TELEGRAM_CHAT_IDS"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "telegram-bot-token",
			Usage:    "Telegram: bot token",
			EnvVars:  []string{"ST_TELEGRAM_BOT_TOKEN"},
			Required: true,
		},
		&cli.StringFlag{
			Name:    "telegram-api-prefix",
			Usage:   "Telegram: API url prefix",
			Value:   "https://api.telegram.org/",
			EnvVars: []string{"ST_TELEGRAM_API_PREFIX"},
		},
		&cli.StringFlag{
			Name:    "message-template",
			Usage:   "Telegram message template",
			Value:   "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
			EnvVars: []string{"ST_TELEGRAM_MESSAGE_TEMPLATE"},
		},
		&cli.Float64Flag{
			Name:    "telegram-api-timeout-seconds",
			Usage:   "HTTP timeout used for requests to the Telegram API",
			Value:   30,
			EnvVars: []string{"ST_TELEGRAM_API_TIMEOUT_SECONDS"},
		},
		&cli.StringFlag{
			Name: "forwarded-attachment-max-size",
			Usage: "Max size of an attachment to be forwarded to telegram. " +
				"0 -- disable forwarding. Examples: 5k, 10m. " +
				"Telegram API has a 50m limit on their side.",
			Value:   "10m",
			EnvVars: []string{"ST_FORWARDED_ATTACHMENT_MAX_SIZE"},
		},
		&cli.StringFlag{
			Name: "forwarded-attachment-max-photo-size",
			Usage: "Max size of a photo attachment to be forwarded to telegram. " +
				"0 -- disable forwarding. Examples: 5k, 10m. " +
				"Telegram API has a 10m limit on their side.",
			Value:   "10m",
			EnvVars: []string{"ST_FORWARDED_ATTACHMENT_MAX_PHOTO_SIZE"},
		},
		&cli.BoolFlag{
			Name: "forwarded-attachment-respect-errors",
			Usage: "Reject the whole email if some attachments " +
				"could not have been forwarded",
			Value:   false,
			EnvVars: []string{"ST_FORWARDED_ATTACHMENT_RESPECT_ERRORS"},
		},
		&cli.UintFlag{
			Name: "message-length-to-send-as-file",
			Usage: "If message length is greater than this number, it is " +
				"sent truncated followed by a text file containing " +
				"the full message. Telegram API has a limit of 4096 chars per message. " +
				"The maximum text file size is determined by `forwarded-attachment-max-size`.",
			Value:   4095,
			EnvVars: []string{"ST_MESSAGE_LENGTH_TO_SEND_AS_FILE"},
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}
}

func SmtpStart(
	smtpConfig *SmtpConfig, telegramConfig *TelegramConfig) (guerrilla.Daemon, error) {

	cfg := &guerrilla.AppConfig{LogFile: log.OutputStdout.String()}

	cfg.AllowedHosts = []string{"."}

	sc := guerrilla.ServerConfig{
		IsEnabled:       true,
		ListenInterface: smtpConfig.smtpListen,
		MaxSize:         smtpConfig.smtpMaxEnvelopeSize,
	}
	cfg.Servers = append(cfg.Servers, sc)

	bcfg := backends.BackendConfig{
		"save_workers_size":  3,
		"save_process":       "HeadersParser|Header|Hasher|TelegramBot",
		"log_received_mails": true,
		"primary_mail_host":  smtpConfig.smtpPrimaryHost,
	}
	cfg.BackendConfig = bcfg

	daemon := guerrilla.Daemon{Config: cfg}
	daemon.AddProcessor("TelegramBot", TelegramBotProcessorFactory(telegramConfig))

	logger = daemon.Log()

	err := daemon.Start()
	return daemon, err
}

func TelegramBotProcessorFactory(
	telegramConfig *TelegramConfig) func() backends.Decorator {
	return func() backends.Decorator {
		// https://github.com/flashmob/go-guerrilla/wiki/Backends,-configuring-and-extending

		return func(p backends.Processor) backends.Processor {
			return backends.ProcessWith(
				func(e *mail.Envelope, task backends.SelectTask) (backends.Result, error) {
					if task == backends.TaskSaveMail {
						err := SendEmailToTelegram(e, telegramConfig)
						if err != nil {
							return backends.NewResult(fmt.Sprintf("554 Error: %s", err)), err
						}
						return p.Process(e, task)
					}
					return p.Process(e, task)
				},
			)
		}
	}
}

func SendEmailToTelegram(e *mail.Envelope,
	telegramConfig *TelegramConfig) error {

	message, err := FormatEmail(e, telegramConfig)
	if err != nil {
		return err
	}

	client := http.Client{
		Timeout: time.Duration(telegramConfig.telegramApiTimeoutSeconds*1000) * time.Millisecond,
	}

	for _, chatId := range strings.Split(telegramConfig.telegramChatIds, ",") {
		fromMail := ""
		if strings.Contains(chatId, ":") {
			parsedChatId := strings.Split(chatId, ":")
			fromMail, chatId = parsedChatId[0], parsedChatId[1]
		}
		logger.Info(fromMail, e.MailFrom.String())
		if !strings.Contains(e.MailFrom.String(), fromMail) {
			continue
		}
		sentMessage, err := SendMessageToChat(message, chatId, telegramConfig, &client)
		if err != nil {
			// If unable to send at least one message -- reject the whole email.
			return errors.New(SanitizeBotToken(err.Error(), telegramConfig.telegramBotToken))
		}

		for _, attachment := range message.attachments {
			err = SendAttachmentToChat(attachment, chatId, telegramConfig, &client, sentMessage)
			if err != nil {
				err = errors.New(SanitizeBotToken(err.Error(), telegramConfig.telegramBotToken))
				if telegramConfig.forwardedAttachmentRespectErrors {
					return err
				} else {
					logger.Errorf("Ignoring attachment sending error: %s", err)
				}
			}
		}
	}
	return nil
}

func SendMessageToChat(
	message *FormattedEmail,
	chatId string,
	telegramConfig *TelegramConfig,
	client *http.Client,
) (*TelegramAPIMessage, error) {
	// The native golang's http client supports
	// http, https and socks5 proxies via HTTP_PROXY/HTTPS_PROXY env vars
	// out of the box.
	//
	// See: https://golang.org/pkg/net/http/#ProxyFromEnvironment
	resp, err := client.PostForm(
		// https://core.telegram.org/bots/api#sendmessage
		fmt.Sprintf(
			"%sbot%s/sendMessage?disable_web_page_preview=true",
			telegramConfig.telegramApiPrefix,
			telegramConfig.telegramBotToken,
		),
		url.Values{"chat_id": {chatId}, "text": {message.text}},
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, errors.New(fmt.Sprintf(
			"Non-200 response from Telegram: (%d) %s",
			resp.StatusCode,
			EscapeMultiLine(body),
		))
	}

	j, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Error reading json body of sendMessage: %v", err)
	}
	result := &TelegramAPIMessageResult{}
	err = json.Unmarshal(j, result)
	if err != nil {
		return nil, fmt.Errorf("Error parsing json body of sendMessage: %v", err)
	}
	if result.Ok != true {
		return nil, fmt.Errorf("ok != true: %s", j)
	}
	return result.Result, nil
}

func SendAttachmentToChat(
	attachment *FormattedAttachment,
	chatId string,
	telegramConfig *TelegramConfig,
	client *http.Client,
	sentMessage *TelegramAPIMessage,
) error {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	var method string
	// https://core.telegram.org/bots/api#sending-files
	if attachment.fileType == ATTACHMENT_TYPE_DOCUMENT {
		// https://core.telegram.org/bots/api#senddocument
		method = "sendDocument"
		panicIfError(w.WriteField("chat_id", chatId))
		panicIfError(w.WriteField("reply_to_message_id", fmt.Sprintf("%s", sentMessage.MessageId)))
		panicIfError(w.WriteField("caption", attachment.caption))
		// TODO maybe reuse files sent to multiple chats via file_id?
		dw, err := w.CreateFormFile("document", attachment.filename)
		panicIfError(err)
		_, err = dw.Write(attachment.content)
		panicIfError(err)
	} else if attachment.fileType == ATTACHMENT_TYPE_PHOTO {
		// https://core.telegram.org/bots/api#sendphoto
		method = "sendPhoto"
		panicIfError(w.WriteField("chat_id", chatId))
		panicIfError(w.WriteField("reply_to_message_id", fmt.Sprintf("%s", sentMessage.MessageId)))
		panicIfError(w.WriteField("caption", attachment.caption))
		// TODO maybe reuse files sent to multiple chats via file_id?
		dw, err := w.CreateFormFile("photo", attachment.filename)
		panicIfError(err)
		_, err = dw.Write(attachment.content)
		panicIfError(err)
	} else {
		panic(fmt.Errorf("Unknown file type %d", attachment.fileType))
	}
	w.Close()

	resp, err := client.Post(
		fmt.Sprintf(
			"%sbot%s/%s?disable_notification=true",
			telegramConfig.telegramApiPrefix,
			telegramConfig.telegramBotToken,
			method,
		),
		w.FormDataContentType(),
		buf,
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		return errors.New(fmt.Sprintf(
			"Non-200 response from Telegram: (%d) %s",
			resp.StatusCode,
			EscapeMultiLine(body),
		))
	}
	return nil
}

func FormatEmail(e *mail.Envelope, telegramConfig *TelegramConfig) (*FormattedEmail, error) {
	reader := e.NewReader()
	env, err := enmime.ReadEnvelope(reader)
	if err != nil {
		return nil, fmt.Errorf("%s\n\nError occurred during email parsing: %v", e, err)
	}
	text := env.Text

	attachmentsDetails := []string{}
	attachments := []*FormattedAttachment{}

	doParts := func(emoji string, parts []*enmime.Part) {
		for _, part := range parts {
			if bytes.Compare(part.Content, []byte(env.Text)) == 0 {
				continue
			}
			if text == "" && part.ContentType == "text/plain" && part.FileName == "" {
				text = string(part.Content)
				continue
			}
			action := "discarded"
			contentType := GuessContentType(part.ContentType, part.FileName)
			if FileIsImage(contentType) && len(part.Content) <= telegramConfig.forwardedAttachmentMaxPhotoSize {
				action = "sending..."
				attachments = append(attachments, &FormattedAttachment{
					filename: part.FileName,
					caption:  part.FileName,
					content:  part.Content,
					fileType: ATTACHMENT_TYPE_PHOTO,
				})
			} else {
				if len(part.Content) <= telegramConfig.forwardedAttachmentMaxSize {
					action = "sending..."
					attachments = append(attachments, &FormattedAttachment{
						filename: part.FileName,
						caption:  part.FileName,
						content:  part.Content,
						fileType: ATTACHMENT_TYPE_DOCUMENT,
					})
				}
			}
			line := fmt.Sprintf(
				"- %s %s (%s) %s, %s",
				emoji,
				part.FileName,
				contentType,
				units.HumanSize(float64(len(part.Content))),
				action,
			)
			attachmentsDetails = append(attachmentsDetails, line)
		}
	}
	doParts("🔗", env.Inlines)
	doParts("📎", env.Attachments)
	for _, part := range env.OtherParts {
		line := fmt.Sprintf(
			"- ❔ %s (%s) %s, discarded",
			part.FileName,
			GuessContentType(part.ContentType, part.FileName),
			units.HumanSize(float64(len(part.Content))),
		)
		attachmentsDetails = append(attachmentsDetails, line)
	}
	for _, e := range env.Errors {
		logger.Errorf("Envelope error: %s", e.Error())
	}

	if text == "" {
		text = e.Data.String()
	}

	formattedAttachmentsDetails := ""
	if len(attachmentsDetails) > 0 {
		formattedAttachmentsDetails = fmt.Sprintf(
			"Attachments:\n%s",
			strings.Join(attachmentsDetails, "\n"),
		)
	}

	fullMessageText, truncatedMessageText := FormatMessage(
		e.MailFrom.String(),
		JoinEmailAddresses(e.RcptTo),
		env.GetHeader("subject"),
		text,
		formattedAttachmentsDetails,
		telegramConfig,
	)
	if truncatedMessageText == "" { // no need to truncate
		return &FormattedEmail{
			text:        fullMessageText,
			attachments: attachments,
		}, nil
	} else {
		if len(fullMessageText) > telegramConfig.forwardedAttachmentMaxSize {
			return nil, fmt.Errorf(
				"The message length (%d) is larger than `forwarded-attachment-max-size` (%d)",
				len(fullMessageText),
				telegramConfig.forwardedAttachmentMaxSize,
			)
		}
		at := &FormattedAttachment{
			filename: "full_message.txt",
			caption:  "Full message",
			content:  []byte(fullMessageText),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		}
		attachments := append([]*FormattedAttachment{at}, attachments...)
		return &FormattedEmail{
			text:        truncatedMessageText,
			attachments: attachments,
		}, nil
	}
}

func FormatMessage(
	from string, to string, subject string, text string,
	formattedAttachmentsDetails string,
	telegramConfig *TelegramConfig,
) (string, string) {
	fullMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(text),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.messageTemplate),
	)
	fullMessageRunes := []rune(fullMessageText)
	if uint(len(fullMessageRunes)) <= telegramConfig.messageLengthToSendAsFile {
		// No need to truncate
		return fullMessageText, ""
	}

	emptyMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(fmt.Sprintf(".%s", BodyTruncated)),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.messageTemplate),
	)
	emptyMessageRunes := []rune(emptyMessageText)
	if uint(len(emptyMessageRunes)) >= telegramConfig.messageLengthToSendAsFile {
		// Impossible to truncate properly
		return fullMessageText, string(fullMessageRunes[:telegramConfig.messageLengthToSendAsFile])
	}

	maxBodyLength := telegramConfig.messageLengthToSendAsFile - uint(len(emptyMessageRunes))
	truncatedMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			// TODO cut by paragraphs + respect formatting
			"{body}", strings.TrimSpace(fmt.Sprintf("%s%s",
				string([]rune(strings.TrimSpace(text))[:maxBodyLength]), BodyTruncated)),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.messageTemplate),
	)
	if uint(len([]rune(truncatedMessageText))) > telegramConfig.messageLengthToSendAsFile {
		panic(fmt.Errorf("Unexpected length of truncated message:\n%d\n%s",
			maxBodyLength, truncatedMessageText))
	}
	return fullMessageText, truncatedMessageText
}

func GuessContentType(contentType string, filename string) string {
	if contentType != "application/octet-stream" {
		return contentType
	}
	guessedType := mime.TypeByExtension(filepath.Ext(filename))
	if guessedType != "" {
		return guessedType
	}
	return contentType // Give up
}

func FileIsImage(contentType string) bool {
	switch contentType {
	case
		// "image/gif",  // sent as a static image
		// "image/x-ms-bmp",  // rendered as document
		"image/jpeg",
		"image/png":
		return true
	}
	return false
}

func JoinEmailAddresses(a []mail.Address) string {
	s := []string{}
	for _, aa := range a {
		s = append(s, aa.String())
	}
	return strings.Join(s, ", ")
}

func EscapeMultiLine(b []byte) string {
	// Apparently errors returned by smtp must not contain newlines,
	// otherwise the data after the first newline is not getting
	// to the parsed message.
	s := string(b)
	s = strings.Replace(s, "\r", "\\r", -1)
	s = strings.Replace(s, "\n", "\\n", -1)
	return s
}

func SanitizeBotToken(s string, botToken string) string {
	return strings.Replace(s, botToken, "***", -1)
}

func panicIfError(err error) {
	if err != nil {
		panic(err)
	}
}

func sigHandler(d guerrilla.Daemon) {
	signalChannel := make(chan os.Signal, 1)

	signal.Notify(signalChannel,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGINT,
		syscall.SIGKILL,
		os.Kill,
	)
	for range signalChannel {
		logger.Info("Shutdown signal caught")
		go func() {
			select {
			// exit if graceful shutdown not finished in 60 sec.
			case <-time.After(time.Second * 60):
				logger.Error("graceful shutdown timed out")
				os.Exit(1)
			}
		}()
		d.Shutdown()
		logger.Info("Shutdown completed, exiting.")
		return
	}
}
