package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/flashmob/go-guerrilla"
	"github.com/stretchr/testify/assert"
	"gopkg.in/gomail.v2"
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
		telegramChatIds:                  "42,142",
		telegramBotToken:                 "42:ZZZ",
		telegramApiPrefix:                "http://" + testHttpServerListen + "/",
		messageTemplate:                  "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
		forwardedAttachmentMaxSize:       0,
		forwardedAttachmentMaxPhotoSize:  0,
		forwardedAttachmentRespectErrors: true,
		messageLengthToSendAsFile:        4095,
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

func goMailBody(content []byte) gomail.FileSetting {
	return gomail.SetCopyFunc(func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
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
			"hi"

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
		"hi"

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
			"💩"
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

func TestAttachmentsDetails(t *testing.T) {
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
	// attachment txt file
	m.Attach("hey.txt", goMailBody([]byte("hi")))
	// inline image
	m.Embed("inline.jpg", goMailBody([]byte("JPG")))
	// attachment image
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, 0)
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body\n" +
			"\n" +
			"Attachments:\n" +
			"- 🔗 inline.jpg (image/jpeg) 3B, discarded\n" +
			"- 📎 hey.txt (text/plain) 2B, discarded\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, discarded"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func TestAttachmentsSending(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
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
	// attachment txt file
	m.Attach("hey.txt", goMailBody([]byte("hi")))
	// inline image
	m.Embed("inline.jpg", goMailBody([]byte("JPG")))
	// attachment image
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "inline.jpg",
			caption:  "inline.jpg",
			content:  []byte("JPG"),
			fileType: ATTACHMENT_TYPE_PHOTO,
		},
		&FormattedAttachment{
			filename: "hey.txt",
			caption:  "hey.txt",
			content:  []byte("hi"),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
		&FormattedAttachment{
			filename: "attachment.jpg",
			caption:  "attachment.jpg",
			content:  []byte("JPG"),
			fileType: ATTACHMENT_TYPE_PHOTO,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body\n" +
			"\n" +
			"Attachments:\n" +
			"- 🔗 inline.jpg (image/jpeg) 3B, sending...\n" +
			"- 📎 hey.txt (text/plain) 2B, sending...\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageAggressivelyTruncated(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.messageLengthToSendAsFile = 12
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hello_", 60))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hello_", 60)
	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "full_message.txt",
			caption:  "Full message",
			content:  []byte(expFull),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, len(strings.Split(telegramConfig.telegramChatIds, ",")))

	exp :=
		"From: from@t"
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageProperlyTruncated(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.messageLengthToSendAsFile = 100
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hello_", 60))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hello_", 60)
	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "full_message.txt",
			caption:  "Full message",
			content:  []byte(expFull),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, len(strings.Split(telegramConfig.telegramChatIds, ",")))

	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Hello_Hello_Hello_Hello_Hello_Hello_He\n" +
			"\n" +
			"[truncated]"
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageWithAttachmentsProperlyTruncated(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.messageLengthToSendAsFile = 150
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hel lo", 60))
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hel lo", 60) +
			"\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "full_message.txt",
			caption:  "Full message",
			content:  []byte(expFull),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
		&FormattedAttachment{
			filename: "attachment.jpg",
			caption:  "attachment.jpg",
			content:  []byte("JPG"),
			fileType: ATTACHMENT_TYPE_PHOTO,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	err := di.DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, 2*len(strings.Split(telegramConfig.telegramChatIds, ",")))

	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Hel loHel loHel loHel loHel\n" +
			"\n" +
			"[truncated]\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestMuttMessagePlaintextParsing(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	// date | mutt -s "test" -a ./tt -- to@test
	m := `Received: from USER by HOST with local (Exim 4.92)
	(envelope-from <from@test>)
	id 111111-000000-OS
	for to@test; Sun, 29 Aug 2021 21:30:10 +0300
Date: Sun, 29 Aug 2021 21:30:10 +0300
From: from@test
To: to@test
Subject: test
Message-ID: <20210829183010.11111111@HOST>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="TB36FDmn/VVEgNH/"
Content-Disposition: inline
User-Agent: Mutt/1.10.1 (2018-07-13)


--TB36FDmn/VVEgNH/
Content-Type: text/plain; charset=us-ascii
Content-Disposition: inline

Sun 29 Aug 2021 09:30:10 PM MSK

--TB36FDmn/VVEgNH/
Content-Type: text/plain; charset=us-ascii
Content-Disposition: attachment; filename=tt

hoho

--TB36FDmn/VVEgNH/--
.`

	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "tt",
			caption:  "tt",
			content:  []byte("hoho\n"),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	ds, err := di.Dial()
	assert.NoError(t, err)
	defer ds.Close()
	err = ds.Send("from@test", []string{"to@test"}, bytes.NewBufferString(m))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: test\n" +
			"\n" +
			"Sun 29 Aug 2021 09:30:10 PM MSK\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 tt (text/plain) 5B, sending..."
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestMailxMessagePlaintextParsing(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.forwardedAttachmentMaxSize = 1024
	telegramConfig.forwardedAttachmentMaxPhotoSize = 1024
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	// date | mail -A ./tt -s "test" to@test
	m := `Received: from USER by HOST with local (Exim 4.92)
	(envelope-from <from@test>)
	id 111111-000000-Bj
	for to@test; Sun, 29 Aug 2021 21:30:23 +0300
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="1493203554-1630261823=:345292"
Subject: test
To: to@test
X-Mailer: mail (GNU Mailutils 3.5)
Message-Id: <2222222-000000-Bj@HOST>
From: from@test
Date: Sun, 29 Aug 2021 21:30:23 +0300

--1493203554-1630261823=:345292
Content-Type: text/plain; charset=UTF-8
Content-Disposition: attachment
Content-Transfer-Encoding: 8bit
Content-ID: <20210829213023.345292.1@HOST>

Sun 29 Aug 2021 09:30:23 PM MSK

--1493203554-1630261823=:345292
Content-Type: application/octet-stream; name="tt"
Content-Disposition: attachment; filename="./tt"
Content-Transfer-Encoding: base64
Content-ID: <20210829213023.345292.1@HOST>

aG9obwo=
--1493203554-1630261823=:345292--
.`

	expFiles := []*FormattedAttachment{
		&FormattedAttachment{
			filename: "tt",
			caption:  "./tt",
			content:  []byte("hoho\n"),
			fileType: ATTACHMENT_TYPE_DOCUMENT,
		},
	}

	di := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "")
	ds, err := di.Dial()
	assert.NoError(t, err)
	defer ds.Close()
	err = ds.Send("from@test", []string{"to@test"}, bytes.NewBufferString(m))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	assert.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: test\n" +
			"\n" +
			"Sun 29 Aug 2021 09:30:23 PM MSK\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 ./tt (application/octet-stream) 5B, sending..."
	assert.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		assert.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLatin1Encoding(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	telegramConfig := makeTelegramConfig()
	d := startSmtp(smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HttpServer(h)
	defer s.Shutdown(context.Background())

	// https://github.com/KostyaEsmukov/smtp_to_telegram/issues/24#issuecomment-980684254
	m := `Date: Sat, 27 Nov 2021 17:31:21 +0100
From: qBittorrent_notification@example.com
Subject: =?ISO-8859-1?Q?Anna-V=E9ronique?=
To: to@test
MIME-Version: 1.0
Content-Type: text/plain; charset=ISO-8859-1
Content-Transfer-Encoding: base64

QW5uYS1W6XJvbmlxdWUK
`
	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(m))
	assert.NoError(t, err)

	assert.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.telegramChatIds, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Anna-Véronique\n" +
			"\n" +
			"Anna-Véronique"
	assert.Equal(t, exp, h.RequestMessages[0])
}

func HttpServer(handler http.Handler) *http.Server {
	h := &http.Server{Addr: testHttpServerListen, Handler: handler}
	ln, err := net.Listen("tcp", h.Addr)
	if err != nil {
		panic(err)
	}
	go func() {
		h.Serve(ln)
	}()
	return h
}

type SuccessHandler struct {
	RequestMessages  []string
	RequestDocuments []*FormattedAttachment
}

func NewSuccessHandler() *SuccessHandler {
	return &SuccessHandler{
		RequestMessages:  []string{},
		RequestDocuments: []*FormattedAttachment{},
	}
}

func (s *SuccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "sendMessage") {
		w.Write([]byte(`{"ok":true,"result":{"message_id": 123123}}`))
		err := r.ParseForm()
		if err != nil {
			panic(err)
		}
		s.RequestMessages = append(s.RequestMessages, r.PostForm.Get("text"))
		return
	}
	isSendDocument := strings.Contains(r.URL.Path, "sendDocument")
	isSendPhoto := strings.Contains(r.URL.Path, "sendPhoto")
	if isSendDocument || isSendPhoto {
		w.Write([]byte(`{}`))
		if r.FormValue("reply_to_message_id") != "123123" {
			panic(fmt.Errorf("Unexpected reply_to_message_id: %s", r.FormValue("reply_to_message_id")))
		}
		err := r.ParseMultipartForm(1024 * 1024)
		if err != nil {
			panic(err)
		}
		key := "document"
		fileType := ATTACHMENT_TYPE_DOCUMENT
		if isSendPhoto {
			key = "photo"
			fileType = ATTACHMENT_TYPE_PHOTO
		}
		file, header, err := r.FormFile(key)
		if err != nil {
			panic(err)
		}
		defer file.Close()
		var buf bytes.Buffer
		io.Copy(&buf, file)
		s.RequestDocuments = append(
			s.RequestDocuments,
			&FormattedAttachment{
				filename: header.Filename,
				caption:  r.FormValue("caption"),
				content:  buf.Bytes(),
				fileType: fileType,
			},
		)
	} else {
		w.WriteHeader(404)
		w.Write([]byte("Error"))
	}
}

type ErrorHandler struct{}

func (s *ErrorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(400)
	w.Write([]byte("Error"))
}
