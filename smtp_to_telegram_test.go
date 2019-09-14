package main

import (
	"context"
	"fmt"
	"github.com/flashmob/go-guerrilla"
	"github.com/stretchr/testify/assert"
	"gopkg.in/gomail.v2"
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

var (
	testSmtpListenHost   = "127.0.0.1"
	testSmtpListenPort   = 22725
	testHttpServerListen = "127.0.0.1:22780"
)

func makeSmtpConfig() *SmtpConfig {
	return &SmtpConfig{
		smtpListen:      fmt.Sprintf("%s:%d", testSmtpListenHost, testSmtpListenPort),
		smtpPrimaryHost: "testhost",
	}
}

func makeTelegramConfig() *TelegramConfig {
	return &TelegramConfig{
		telegramChatIds:   "42,142",
		telegramBotToken:  "42:ZZZ",
		telegramApiPrefix: "http://" + testHttpServerListen + "/",
		messageTemplate:   "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}",
	}
}

func startSmtp(smtpConfig *SmtpConfig, telegramConfig *TelegramConfig) guerrilla.Daemon {
	d, err := SmtpStart(smtpConfig, telegramConfig)
	if err != nil {
		panic(fmt.Sprintf("start error: %s", err))
	}
	waitSmtp(smtpConfig.smtpListen)
	return d
}

func waitSmtp(smtpHost string) {
	for n := 0; n < 100; n++ {
		c, err := smtp.Dial(smtpHost)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSuccess(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: \n" +
			"\n" +
			"hi\n"

	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestSuccessCustomFormat(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.messageTemplate =
		"Subject: {subject}\\n\\n{body}"
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp := "Subject: \n" +
		"\n" +
		"hi\n"

	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestTelegramUnreachable(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestTelegramHttpError(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	s := HttpServer(&ErrorHandler{})
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestEncodedContent(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	b := []byte(
		"Subject: =?UTF-8?B?8J+Yjg==?=\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"Content-Transfer-Encoding: quoted-printable\r\n" +
			"\r\n" +
			"=F0=9F=92=A9\r\n")
	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, b)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: 😎\n" +
			"\n" +
			"💩\n"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestHtmlAttachmentIsIgnored(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.AddAlternative("text/html", "<p>HTML body</p>")

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func HttpServer(handler http.Handler) *http.Server {
	h := &http.Server{Addr: testHttpServerListen, Handler: handler}
	go func() {
		h.ListenAndServe()
	}()
	return h
}

type SuccessHandler struct {
	RequestMessages []string
}

func NewSuccessHandler() *SuccessHandler {
	return &SuccessHandler{RequestMessages: []string{}}
}

func (s *SuccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("OK"))
	err := r.ParseForm()
	if err == nil {
		s.RequestMessages = append(s.RequestMessages, r.PostForm.Get("text"))
	}
}

type ErrorHandler struct{}

func (s *ErrorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(400)
	w.Write([]byte("Error"))
}
