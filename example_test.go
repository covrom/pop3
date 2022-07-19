package pop3_test

import (
	"context"
	"time"

	"log"

	"github.com/covrom/pop3"
)

func ExamplePOP3Conn() {
	cfg := pop3.POP3ClientConfig{
		Host:          "pop.mail.com",
		Port:          "995", // "110"
		Username:      "tttest@mail.com",
		Password:      "",
		Timeout:       time.Minute,
		TLSEnabled:    true,
		TLSSkipVerify: false,
	}
	if err := pop3.PopMails(context.Background(), cfg, func(msg *pop3.MailMessage) error {
		log.Println(msg.From, msg.Subject, msg.Text)
		return nil
	}); err != nil {
		log.Println(err)
	}
}
