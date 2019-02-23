package main

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"gopkg.in/gomail.v2"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	testSmtpListenHost    = "127.0.0.1"
	testSmtpListenPort    = 22725
	testSmtpListen        = fmt.Sprintf("%s:%d", testSmtpListenHost, testSmtpListenPort)
	testSmtpPrimaryHost   = "testhost"
	testTelegramChatIds   = "42,142"
	testTelegramBotToken  = "42:ZZZ"
	testHttpServerListen  = "127.0.0.1:22780"
	testTelegramApiPrefix = "http://" + testHttpServerListen + "/"
)

func TestMain(m *testing.M) {
	setUp()
	retCode := m.Run()
	tearDown()
	os.Exit(retCode)
}

func setUp() {
	err := SmtpStart(
		testSmtpListen,
		testSmtpPrimaryHost,
		testTelegramChatIds,
		testTelegramBotToken,
		testTelegramApiPrefix,
	)
	if err != nil {
		panic(fmt.Sprintf("start error: %s", err))
	}
	waitSmtp(testSmtpListen)
}

func tearDown() {
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
	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(testSmtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(testTelegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: \n" +
			"\n" +
			"hi\n"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestTelegramUnreachable(t *testing.T) {
	err := smtp.SendMail(testSmtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestTelegramHttpError(t *testing.T) {
	s := HttpServer(&ErrorHandler{})
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(testSmtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestEncodedContent(t *testing.T) {
	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	b := []byte(
		"Subject: =?UTF-8?B?8J+Yjg==?=\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"Content-Transfer-Encoding: quoted-printable\r\n" +
			"\r\n" +
			"=F0=9F=92=A9\r\n")
	err := smtp.SendMail(testSmtpListen, nil, "from@test", []string{"to@test"}, b)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(testTelegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: 😎\n" +
			"\n" +
			"💩\n"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestHtmlAttachmentIsIgnored(t *testing.T) {
	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.AddAlternative("text/html", "<p>HTML body</p>")

	d := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := d.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(testTelegramChatIds, ",")))
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
