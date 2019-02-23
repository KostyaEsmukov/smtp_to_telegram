package main

import (
	"errors"
	"fmt"
	"github.com/flashmob/go-guerrilla"
	"github.com/flashmob/go-guerrilla/backends"
	"github.com/flashmob/go-guerrilla/log"
	"github.com/flashmob/go-guerrilla/mail"
	"github.com/jhillyerd/enmime"
	"github.com/urfave/cli"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	d guerrilla.Daemon
)

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
	app.Action = func(c *cli.Context) error {
		// Required flags are not supported, see https://github.com/urfave/cli/issues/85
		if !c.IsSet("telegram-chat-ids") {
			return cli.NewExitError("Telegram chat ids are missing. See `--help`", 2)
		}
		if !c.IsSet("telegram-bot-token") {
			return cli.NewExitError("Telegram bot token is missing. See `--help`", 2)
		}
		err := SmtpStart(
			c.String("smtp-listen"),
			c.String("smtp-primary-host"),
			c.String("telegram-chat-ids"),
			c.String("telegram-bot-token"),
			c.String("telegram-api-prefix"),
		)
		if err != nil {
			panic(fmt.Sprintf("start error: %s", err))
		}
		sigHandler()
		return nil
	}
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "smtp-listen",
			Value:  "127.0.0.1:2525",
			Usage:  "SMTP: TCP address to listen to",
			EnvVar: "ST_SMTP_LISTEN",
		},
		cli.StringFlag{
			Name:   "smtp-primary-host",
			Value:  GetHostname(),
			Usage:  "SMTP: primary host",
			EnvVar: "ST_SMTP_PRIMARY_HOST",
		},
		cli.StringFlag{
			Name:   "telegram-chat-ids",
			Usage:  "Telegram: comma-separated list of chat ids",
			EnvVar: "ST_TELEGRAM_CHAT_IDS",
		},
		cli.StringFlag{
			Name:   "telegram-bot-token",
			Usage:  "Telegram: bot token",
			EnvVar: "ST_TELEGRAM_BOT_TOKEN",
		},
		cli.StringFlag{
			Name:   "telegram-api-prefix",
			Usage:  "Telegram: API url prefix",
			Value:  "https://api.telegram.org/",
			EnvVar: "ST_TELEGRAM_API_PREFIX",
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		os.Exit(1)
	}
}

func SmtpStart(
	smtpListen string, smtpPrimaryHost string, telegramChatIds string,
	telegramBotToken string, telegramApiPrefix string) error {

	cfg := &guerrilla.AppConfig{LogFile: log.OutputStdout.String()}

	cfg.AllowedHosts = []string{"."}

	sc := guerrilla.ServerConfig{
		ListenInterface: smtpListen,
		IsEnabled:       true,
	}
	cfg.Servers = append(cfg.Servers, sc)

	bcfg := backends.BackendConfig{
		"save_workers_size":  3,
		"save_process":       "HeadersParser|Header|Hasher|TelegramBot",
		"log_received_mails": true,
		"primary_mail_host":  smtpPrimaryHost,
	}
	cfg.BackendConfig = bcfg

	d = guerrilla.Daemon{Config: cfg}
	d.AddProcessor("TelegramBot", TelegramBotProcessorFactory(
		telegramChatIds, telegramBotToken, telegramApiPrefix))

	err := d.Start()
	return err
}

func TelegramBotProcessorFactory(
	telegramChatIds string, telegramBotToken string,
	telegramApiPrefix string) func() backends.Decorator {
	return func() backends.Decorator {
		// https://github.com/flashmob/go-guerrilla/wiki/Backends,-configuring-and-extending

		return func(p backends.Processor) backends.Processor {
			return backends.ProcessWith(
				func(e *mail.Envelope, task backends.SelectTask) (backends.Result, error) {
					if task == backends.TaskSaveMail {
						err := SendEmailToTelegram(
							e, telegramChatIds, telegramBotToken, telegramApiPrefix)
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
	telegramChatIds string, telegramBotToken string, telegramApiPrefix string) error {

	message := FormatEmail(e)

	for _, chatId := range strings.Split(telegramChatIds, ",") {

		// Apparently the native golang's http client supports
		// http, https and socks5 proxies via HTTP_PROXY/HTTPS_PROXY env vars
		// out of the box.
		//
		// See: https://golang.org/pkg/net/http/#ProxyFromEnvironment
		resp, err := http.PostForm(
			fmt.Sprintf(
				"%sbot%s/sendMessage?disable_web_page_preview=true",
				telegramApiPrefix,
				telegramBotToken,
			),
			url.Values{"chat_id": {chatId}, "text": {message}},
		)

		if err != nil {
			return errors.New(SanitizeBotToken(err.Error(), telegramBotToken))
		}
		if resp.StatusCode != 200 {
			body, _ := ioutil.ReadAll(resp.Body)
			return errors.New(fmt.Sprintf(
				"Non-200 response from Telegram: (%d) %s",
				resp.StatusCode,
				SanitizeBotToken(EscapeMultiLine(body), telegramBotToken),
			))
		}
	}
	return nil
}

func FormatEmail(e *mail.Envelope) string {
	reader := e.NewReader()
	env, err := enmime.ReadEnvelope(reader)
	if err != nil {
		return fmt.Sprintf("%s\n\nError occurred during email parsing: %s", e, err)
	}
	text := env.Text
	if text == "" {
		text = e.Data.String()
	}
	return fmt.Sprintf(
		"From: %s\nTo: %s\nSubject: %s\n\n%s",
		e.MailFrom.String(),
		MapAddresses(e.RcptTo),
		env.GetHeader("subject"),
		text,
	)
}

func MapAddresses(a []mail.Address) string {
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

func sigHandler() {
	signalChannel := make(chan os.Signal, 1)

	signal.Notify(signalChannel,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGINT,
		syscall.SIGKILL,
		os.Kill,
	)
	for range signalChannel {
		d.Log().Infof("Shutdown signal caught")
		go func() {
			select {
			// exit if graceful shutdown not finished in 60 sec.
			case <-time.After(time.Second * 60):
				d.Log().Error("graceful shutdown timed out")
				os.Exit(1)
			}
		}()
		d.Shutdown()
		d.Log().Infof("Shutdown completed, exiting.")
		return
	}
}
