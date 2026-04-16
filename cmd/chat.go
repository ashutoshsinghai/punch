package cmd

import (
	"os/user"

	"github.com/ashutoshsinghai/punch/internal/crypto"
	"github.com/ashutoshsinghai/punch/internal/punch"
	"github.com/ashutoshsinghai/punch/ui"
	tea "github.com/charmbracelet/bubbletea"
)

func runChat(result *punch.Result, session string) error {
	cipher, err := crypto.NewCipher(session)
	if err != nil {
		return err
	}

	myName := localUsername()
	peerName := "peer"

	sendFn := func(msg string) error {
		encrypted, err := cipher.Encrypt([]byte(msg))
		if err != nil {
			return err
		}
		return result.Conn.Send(encrypted)
	}

	prog := ui.NewChat(myName, peerName, sendFn)

	go func() {
		for {
			raw, err := result.Conn.Recv()
			if err != nil {
				return
			}
			plaintext, err := cipher.Decrypt(raw)
			if err != nil {
				continue
			}
			prog.Send(ui.IncomingMsg{From: peerName, Body: string(plaintext)})
		}
	}()

	if _, err := prog.Run(); err != nil && err != tea.ErrProgramKilled {
		return err
	}
	result.Conn.Close()
	return nil
}

func localUsername() string {
	u, err := user.Current()
	if err != nil {
		return "me"
	}
	if u.Username != "" {
		return u.Username
	}
	return "me"
}
