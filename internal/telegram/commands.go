package telegram

import (
	"encoding/json"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type handler func(b *Bot, chatID int64, userID int64, username string, arg string) (responses, error)

type command struct {
	tgbotapi.BotCommand
	text     string
	keyboard *tgbotapi.InlineKeyboardMarkup
	handler  handler
}

var (
	StartCmd = command{
		BotCommand: tgbotapi.BotCommand{
			Command:     "start",
			Description: "Главное меню",
		},
		text: "Добро пожаловать! Используйте меню для навигации.",
	}
	MenuCmd = command{
		BotCommand: tgbotapi.BotCommand{
			Command:     "menu",
			Description: "Меню бота",
		},
		text: "Выберите действие:",
	}
	HelpCmd = command{
		BotCommand: tgbotapi.BotCommand{
			Command:     "help",
			Description: "Помощь",
		},
		text: "ℹ️ Доступные команды:\n\n" +
			"/start - Главное меню\n" +
			"/menu - Меню бота\n" +
			"/newkeys - Создать новое устройство (требуется активная подписка)\n" +
			"/help - Показать эту справку",
	}
	ConfigForNewKeysCmd = command{
		BotCommand: tgbotapi.BotCommand{
			Command:     "newkeys",
			Description: "Создать новое устройство",
		},
		text: "",
	}
	AdminCmd = command{
		BotCommand: tgbotapi.BotCommand{
			Command:     "admin",
			Description: "Админ-панель",
		},
		text: "",
	}
)

var commands = map[string]*command{
	StartCmd.Command:             &StartCmd,
	MenuCmd.Command:              &MenuCmd,
	ConfigForNewKeysCmd.Command:  &ConfigForNewKeysCmd,
	HelpCmd.Command:              &HelpCmd,
	AdminCmd.Command:             &AdminCmd,
}

// setMyCommands sets bot commands
func (b *Bot) setMyCommands() error {
	params := make(tgbotapi.Params)
	data, err := json.Marshal([]tgbotapi.BotCommand{
		StartCmd.BotCommand,
		MenuCmd.BotCommand,
		ConfigForNewKeysCmd.BotCommand,
		HelpCmd.BotCommand,
	})
	if err != nil {
		return err
	}
	params.AddNonEmpty("commands", string(data))
	_, err = b.api.MakeRequest("setMyCommands", params)
	if err != nil {
		return err
	}
	return nil
}
