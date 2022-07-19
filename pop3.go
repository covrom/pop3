package pop3

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/jhillyerd/enmime"
)

type POP3ClientConfig struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	Username string `yaml:"user"`
	Password string `yaml:"pass"`

	// Default is 30 seconds.
	Timeout time.Duration `yaml:"timeout"`

	TLSEnabled    bool `yaml:"tls_enabled"`
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
}

type ListItem struct {
	Id   int
	Size int
}

type Mail struct {
	RawMessage []byte
	Message    *mail.Message
	UID        []byte
}

type POP3Conn struct {
	conn    net.Conn
	r       *textproto.Reader
	w       *bufio.Writer
	timeout time.Duration
	lines   []string
	err     error
}

func NewPOP3Conn(cfg POP3ClientConfig) (*POP3Conn, error) {
	if cfg.Timeout < time.Millisecond {
		cfg.Timeout = time.Second * 30
	}
	conn, err := net.DialTimeout("tcp", cfg.Host+":"+cfg.Port, cfg.Timeout)
	if err != nil {
		return nil, err
	}

	if cfg.TLSEnabled {
		// nolint
		tlsCfg := tls.Config{}

		if cfg.TLSSkipVerify {
			tlsCfg.InsecureSkipVerify = true
		} else {
			tlsCfg.ServerName = cfg.Host
		}

		conn = tls.Client(conn, &tlsCfg)
	}

	c := &POP3Conn{
		conn:    conn,
		r:       textproto.NewReader(bufio.NewReader(conn)),
		w:       bufio.NewWriter(conn),
		timeout: cfg.Timeout,
		lines:   make([]string, 0, 100),
	}

	err = c.
		ReadOK("welcome message").
		Sendf("USER %s\r\n", cfg.Username).
		ReadOK("send login").
		Sendf("PASS %s\r\n", cfg.Password).
		ReadOK("send password").
		CloseOnErr()
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *POP3Conn) CloseOnErr() error {
	if c.err != nil {
		_, _ = c.conn.Write([]byte("QUIT\r\n"))
		c.w.Flush()
		c.conn.Close()
	}
	return c.err
}

func (c *POP3Conn) ReadOK(errstr string) *POP3Conn {
	if c.err != nil {
		return c
	}
	s, err := c.r.ReadLine()
	if err != nil {
		c.err = err
	} else if len(s) < 3 || s[0:3] != "+OK" {
		c.err = fmt.Errorf("%s: response is %s", errstr, s)
	}
	return c
}

func (c *POP3Conn) Sendf(format string, args ...interface{}) *POP3Conn {
	if c.err != nil {
		return c
	}
	_, err := fmt.Fprintf(c.w, format, args...)
	if err != nil {
		c.err = err
	}
	c.w.Flush()
	return c
}

func (c *POP3Conn) ReadLinesToPoint(ctx context.Context) *POP3Conn {
	if c.err != nil {
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	c.lines = c.lines[:0]

	line := make(chan string, 10)

	go func() {
		defer close(line)
		for {
			ln, err := c.r.ReadLine()

			if err != nil {
				c.err = err
				return
			}

			if ln == "." {
				return
			}

			select {
			case line <- ln:
			case <-ctx.Done():
				c.err = ctx.Err()
				return
			}
		}
	}()

	for ln := range line {
		c.lines = append(c.lines, ln)
	}
	return c
}

func (c *POP3Conn) List(ctx context.Context) ([]ListItem, error) {
	err := c.
		Sendf("LIST\r\n").
		ReadOK("list mails").
		ReadLinesToPoint(ctx).
		CloseOnErr()
	if err != nil {
		return nil, err
	}

	lines := c.lines
	retv := make([]ListItem, len(lines))
	for i, s := range lines {
		l := strings.Split(s, " ")
		if len(l) < 2 {
			return retv, fmt.Errorf("list line %d is bad: %s", i, s)
		}
		retv[i].Id, err = strconv.Atoi(l[0])
		if err != nil {
			return retv, err
		}
		retv[i].Size, err = strconv.Atoi(l[1])
		if err != nil {
			return retv, err
		}
	}
	return retv, nil
}

func (c *POP3Conn) Delete(id int) error {
	return c.
		Sendf("DELE %d\r\n", id).
		ReadOK("delete message").
		CloseOnErr()
}

type MailMessage struct {
	From,
	Subject,
	Text string
}

func (c *POP3Conn) GetMail(ctx context.Context, id int) (*MailMessage, error) {
	err := c.
		Sendf("RETR %d\r\n", id).
		ReadOK("download mail").
		CloseOnErr()
	if err != nil {
		return nil, err
	}

	msg, err := enmime.ReadEnvelope(c.r.DotReader())
	if err != nil {
		return nil, fmt.Errorf("failed to read message: %w", err)
	}

	return &MailMessage{
		From:    msg.GetHeader("From"),
		Subject: msg.GetHeader("Subject"),
		Text:    strings.TrimSpace(msg.Text),
	}, nil
}

func (c *POP3Conn) Close() {
	c.w.Flush()
	_, _ = c.conn.Write([]byte("QUIT\r\n"))
	c.conn.Close()
}

func PopMails(ctx context.Context, cfg POP3ClientConfig, f func(*MailMessage) error) error {
	cli, err := NewPOP3Conn(cfg)
	if err != nil {
		return err
	}

	defer cli.Close()

	mails, err := cli.List(ctx)
	if err != nil {
		return err
	}

	for _, m := range mails {
		msg, err := cli.GetMail(ctx, m.Id)
		if err != nil {
			return err
		}
		if err := f(msg); err != nil {
			return err
		}

		if err := cli.Delete(m.Id); err != nil {
			return err
		}
	}
	return nil
}
