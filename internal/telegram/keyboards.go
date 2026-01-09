package telegram

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (cmd command) button() tgbotapi.InlineKeyboardButton {
	return tgbotapi.NewInlineKeyboardButtonData(cmd.Description, cmd.Command)
}

var (
	mainMenuKeyboard = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💳 Оплата/Продление", "payment"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Я оплатил", "payment_proof"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📱 Создать устройство", ConfigForNewKeysCmd.Command),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("ℹ️ Помощь", HelpCmd.Command),
		),
	)

	goToMenuButton = tgbotapi.NewInlineKeyboardButtonData("◀️ Меню", MenuCmd.Command)

	helpKeyboard = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(goToMenuButton),
	)

	// Payment duration selection
	durationKeyboard = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("30 дней", "duration:30"),
			tgbotapi.NewInlineKeyboardButtonData("90 дней", "duration:90"),
			tgbotapi.NewInlineKeyboardButtonData("180 дней", "duration:180"),
		),
		tgbotapi.NewInlineKeyboardRow(goToMenuButton),
	)

	// Device count keyboard factory
	deviceCountKeyboardForDuration = func(duration int) *tgbotapi.InlineKeyboardMarkup {
		return &tgbotapi.InlineKeyboardMarkup{
			InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
				{
					tgbotapi.NewInlineKeyboardButtonData("1", fmt.Sprintf("devices:1:%d", duration)),
					tgbotapi.NewInlineKeyboardButtonData("2", fmt.Sprintf("devices:2:%d", duration)),
					tgbotapi.NewInlineKeyboardButtonData("3", fmt.Sprintf("devices:3:%d", duration)),
				},
				{
					tgbotapi.NewInlineKeyboardButtonData("4", fmt.Sprintf("devices:4:%d", duration)),
					tgbotapi.NewInlineKeyboardButtonData("5", fmt.Sprintf("devices:5:%d", duration)),
				},
				{goToMenuButton},
			},
		}
	}

	// Admin keyboard
	adminKeyboard = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 Ожидающие оплаты", "admin:pending"),
		),
		tgbotapi.NewInlineKeyboardRow(goToMenuButton),
	)
)

func init() {
	StartCmd.keyboard = &mainMenuKeyboard
	MenuCmd.keyboard = &mainMenuKeyboard
	HelpCmd.keyboard = &helpKeyboard
}
