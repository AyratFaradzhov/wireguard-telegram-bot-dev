package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	"github.com/yeqown/go-qrcode"

	"github.com/skoret/wireguard-bot/internal/storage"
)

type responses []tgbotapi.Chattable

func (b *Bot) handleMessage(msg *tgbotapi.Message) (responses, error) {
	log.Printf("new message: %+v", msg)

	// Handle photo/document uploads (for payment proof)
	if msg.Photo != nil && len(msg.Photo) > 0 {
		return b.handlePhoto(msg)
	}
	if msg.Document != nil {
		return b.handleDocument(msg)
	}

	if !msg.IsCommand() {
		// Check if user is in payment proof mode (could be implemented with state machine)
		// For now, just show menu
		return responses{tgbotapi.NewMessage(msg.Chat.ID, "Используйте команды из меню или нажмите /menu")}, nil
	}

	cmd, ok := commands[msg.Command()]
	if !ok {
		return responses{tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная команда. Используйте /menu")}, nil
	}

	// Get or create user
	ctx := context.Background()
	user, err := b.repo.GetOrCreateUser(ctx, int64(msg.From.ID), msg.From.UserName)
	if err != nil {
		return responses{errorMessage(msg.Chat.ID, msg.MessageID, false)}, errors.Wrap(err, "failed to get/create user")
	}

	// Register admin if this is /start command and user is admin
	if msg.Command() == "start" && msg.From.UserName != "" {
		b.registerAdmin(msg.From.UserName, msg.Chat.ID)
	}

	res0 := tgbotapi.NewMessage(msg.Chat.ID, cmd.text)
	res0.ReplyMarkup = cmd.keyboard

	if cmd.handler == nil {
		return responses{res0}, nil
	}

	res1, err := cmd.handler(b, msg.Chat.ID, user.ID, user.Username, msg.CommandArguments())
	if err != nil {
		return responses{errorMessage(msg.Chat.ID, msg.MessageID, false)}, err
	}
	if res1 == nil {
		return responses{res0}, nil
	}
	return append(responses{res0}, res1...), nil
}

func (b *Bot) handlePhoto(msg *tgbotapi.Message) (responses, error) {
	// Handle payment proof photo
	ctx := context.Background()
	user, err := b.repo.GetUserByTelegramID(ctx, int64(msg.From.ID))
	if err != nil || user == nil {
		return responses{tgbotapi.NewMessage(msg.Chat.ID, "Ошибка: пользователь не найден")}, err
	}

	// Get the largest photo
	photo := msg.Photo[len(msg.Photo)-1]
	fileID := photo.FileID

	var pendingPayment *storage.Payment

	// First, try to find payment by reference code in caption (if provided)
	if msg.Caption != "" {
		referenceCode := strings.TrimSpace(msg.Caption)
		payment, err := b.repo.GetPaymentByReferenceCode(ctx, referenceCode)
		if err == nil && payment != nil {
			// Verify it belongs to this user and is in correct status
			if payment.UserID == user.ID && payment.Status == storage.PaymentStatusCreated {
				pendingPayment = payment
			}
		}
	}

	// If not found by reference code, find latest payment with status "created" for this user
	if pendingPayment == nil {
		payments, err := b.repo.GetPaymentsByUserIDAndStatus(ctx, user.ID, storage.PaymentStatusCreated)
		if err == nil && len(payments) > 0 {
			// Get the most recent payment with status "created"
			pendingPayment = payments[len(payments)-1]
		}
	}

	if pendingPayment == nil {
		return responses{tgbotapi.NewMessage(msg.Chat.ID, 
			"❌ Не найдена ожидающая оплата со статусом 'создана'.\n\n"+
			"Создайте заявку через меню 'Оплата/Продление', затем отправьте скриншот подтверждения оплаты.\n\n"+
			"Вы также можете указать код заявки в подписи к фото.")}, nil
	}

	// Verify payment status is still "created" (hasn't been processed yet)
	if pendingPayment.Status != storage.PaymentStatusCreated {
		return responses{tgbotapi.NewMessage(msg.Chat.ID, 
			fmt.Sprintf("❌ Платеж с кодом `%s` уже обработан (статус: %s).", 
				pendingPayment.ReferenceCode, pendingPayment.Status))}, nil
	}

	// Attach proof to payment and move to pending_review
	if err := b.billing.AttachProofAndMoveToPendingReview(ctx, pendingPayment.ID, fileID); err != nil {
		return responses{tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при сохранении подтверждения оплаты")}, err
	}

	text := fmt.Sprintf("✅ Подтверждение оплаты получено!\n\n"+
		"Ваша заявка отправлена на проверку администратору.\n"+
		"Код заявки: `%s`\n\n"+
		"После одобрения администратором вы получите уведомление и сможете создать устройства.",
		pendingPayment.ReferenceCode)

	return responses{tgbotapi.NewMessage(msg.Chat.ID, text)}, nil
}

func (b *Bot) handleDocument(msg *tgbotapi.Message) (responses, error) {
	// Similar to handlePhoto but for documents
	return b.handlePhoto(msg)
}

func (b *Bot) handleQuery(query *tgbotapi.CallbackQuery) (responses, error) {
	log.Printf("new callback query: %+v", query)

	if query.Message == nil {
		return nil, errors.New("callback query received without message")
	}

	chatID, msgID := query.Message.Chat.ID, query.Message.MessageID
	ctx := context.Background()

	// Get or create user
	user, err := b.repo.GetOrCreateUser(ctx, int64(query.From.ID), query.From.UserName)
	if err != nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to get/create user")
	}

	callback := tgbotapi.NewCallback(query.ID, "")
	if _, err := b.api.Request(callback); err != nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to process callback query")
	}

	// Handle callback data
	data := query.Data
	resps, err := b.handleCallbackData(ctx, chatID, msgID, user, data)
	if err != nil {
		return responses{errorMessage(chatID, msgID, true)}, err
	}

	return resps, nil
}

func (b *Bot) handleCallbackData(ctx context.Context, chatID int64, msgID int, user *storage.User, data string) (responses, error) {
	log.Printf("handleCallbackData: data='%s', user=%s, chat_id=%d", data, user.Username, chatID)
	
	// Handle menu commands
	if cmd, ok := commands[data]; ok {
		res0 := tgbotapi.NewEditMessageText(chatID, msgID, cmd.text)
		res0.ReplyMarkup = cmd.keyboard
		if cmd.handler == nil {
			return responses{res0}, nil
		}
		res1, err := cmd.handler(b, chatID, user.ID, user.Username, "")
		if err != nil {
			return responses{res0}, err
		}
		return append(responses{res0}, res1...), nil
	}

	// Handle payment proof FIRST (before payment prefix check)
	if data == "payment_proof" {
		log.Printf("Handling payment_proof callback for user %s (chat_id: %d, msg_id: %d)", user.Username, chatID, msgID)
		resps, err := b.handlePaymentProof(ctx, chatID, msgID, user)
		if err != nil {
			log.Printf("ERROR in handlePaymentProof: %v", err)
		} else {
			log.Printf("handlePaymentProof returned %d responses", len(resps))
		}
		return resps, err
	}

	// Handle payment flow (but not payment_proof, which is handled above)
	if strings.HasPrefix(data, "payment") {
		return b.handlePaymentFlow(ctx, chatID, msgID, user, data)
	}

	// Handle duration selection
	if strings.HasPrefix(data, "duration:") {
		durationStr := strings.TrimPrefix(data, "duration:")
		duration, _ := strconv.Atoi(durationStr)
		return b.handleDurationSelection(ctx, chatID, msgID, user, duration)
	}

	// Handle device count selection
	if strings.HasPrefix(data, "devices:") {
		parts := strings.Split(strings.TrimPrefix(data, "devices:"), ":")
		deviceCount, _ := strconv.Atoi(parts[0])
		duration := 30 // Default
		if len(parts) > 1 {
			duration, _ = strconv.Atoi(parts[1])
		}
		return b.handleDeviceCountSelection(ctx, chatID, msgID, user, deviceCount, duration)
	}

	// Handle admin callbacks
	if strings.HasPrefix(data, "admin:") {
		return b.handleAdminCallback(ctx, chatID, msgID, user, data)
	}

	// Handle admin payment approval/rejection (simplified flow)
	if strings.HasPrefix(data, "admin_approve:") {
		paymentIDStr := strings.TrimPrefix(data, "admin_approve:")
		paymentID, _ := strconv.ParseInt(paymentIDStr, 10, 64)
		return b.handleAdminApprovePayment(ctx, chatID, msgID, user, paymentID)
	}

	if strings.HasPrefix(data, "admin_reject:") {
		paymentIDStr := strings.TrimPrefix(data, "admin_reject:")
		paymentID, _ := strconv.ParseInt(paymentIDStr, 10, 64)
		return b.handleAdminRejectPayment(ctx, chatID, msgID, user, paymentID)
	}

	// Handle payment approval/rejection (legacy)
	if strings.HasPrefix(data, "approve:") {
		parts := strings.Split(strings.TrimPrefix(data, "approve:"), ":")
		paymentID, _ := strconv.ParseInt(parts[0], 10, 64)
		// If comment is provided in callback (for quick approve with pre-filled)
		verifiedComment := ""
		if len(parts) > 1 {
			// Join remaining parts in case comment contains ":"
			verifiedComment = strings.Join(parts[1:], ":")
		}
		return b.handleApprovePayment(ctx, chatID, msgID, user, paymentID, verifiedComment)
	}

	if strings.HasPrefix(data, "reject:") {
		paymentIDStr := strings.TrimPrefix(data, "reject:")
		paymentID, _ := strconv.ParseInt(paymentIDStr, 10, 64)
		return b.handleRejectPayment(ctx, chatID, msgID, user, paymentID)
	}

	if strings.HasPrefix(data, "payment_detail:") {
		paymentIDStr := strings.TrimPrefix(data, "payment_detail:")
		paymentID, _ := strconv.ParseInt(paymentIDStr, 10, 64)
		return b.handlePaymentDetail(ctx, chatID, msgID, user, paymentID)
	}

	if strings.HasPrefix(data, "approve_verify:") {
		paymentIDStr := strings.TrimPrefix(data, "approve_verify:")
		paymentID, _ := strconv.ParseInt(paymentIDStr, 10, 64)
		return b.handleApprovePaymentVerify(ctx, chatID, msgID, user, paymentID)
	}

	return responses{errorMessage(chatID, msgID, true)}, errors.Errorf("unknown callback data: %s", data)
}

func (b *Bot) handlePaymentFlow(ctx context.Context, chatID int64, msgID int, user *storage.User, data string) (responses, error) {
	if data == "payment" {
		// Show duration selection
		text := "Выберите срок подписки:"
		res := tgbotapi.NewEditMessageText(chatID, msgID, text)
		res.ReplyMarkup = &durationKeyboard
		return responses{res}, nil
	}
	return nil, nil
}

func (b *Bot) handleDurationSelection(ctx context.Context, chatID int64, msgID int, user *storage.User, duration int) (responses, error) {
	text := fmt.Sprintf("Выбран срок: %d дней\n\nВыберите количество устройств:", duration)
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ReplyMarkup = deviceCountKeyboardForDuration(duration)

	return responses{res}, nil
}

func (b *Bot) handleDeviceCountSelection(ctx context.Context, chatID int64, msgID int, user *storage.User, deviceCount int, duration int) (responses, error) {
	amount := b.billing.CalculatePrice(duration, deviceCount)
	
	// Create payment attempt
	payment, err := b.billing.CreatePaymentAttempt(ctx, user.ID, duration, deviceCount)
	if err != nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to create payment")
	}

	// Simplified payment flow message
	text := fmt.Sprintf("💳 Оплата подписки\n\n"+
		"📋 Детали заявки:\n"+
		"• Срок: %d дней\n"+
		"• Устройств: %d\n"+
		"• Сумма: %.2f руб.\n\n"+
		"🔑 КОД ЗАЯВКИ:\n"+
		"`%s`\n\n"+
		"━━━━━━━━━━━━━━━━━━━━\n\n"+
		"📝 Инструкция:\n"+
		"1. Отсканируйте QR-код ниже\n"+
		"2. Оплатите нужную сумму\n"+
		"3. В комментарии к переводу укажите КОД ЗАЯВКИ\n"+
		"4. После оплаты нажмите «Я оплатил»\n\n"+
		"⚠️ БЕЗ КОДА ЗАЯВКИ ПЛАТЕЖ НЕ БУДЕТ ПРИНЯТ!",
		duration, deviceCount, float64(amount)/100.0, payment.ReferenceCode)

	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ParseMode = "Markdown"
	
	// Keyboard with buttons
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Я оплатил", "payment_proof"),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", MenuCmd.Command),
		),
	)
	res.ReplyMarkup = &keyboard
	
	// Send static QR code from file
	qrPhoto := b.sendPaymentQR(chatID)
	if qrPhoto == nil {
		// If QR failed to load, show error message
		errorMsg := tgbotapi.NewEditMessageText(chatID, msgID, 
			"❌ Ошибка: QR-код не найден. Обратитесь к администратору.")
		return responses{errorMsg}, nil
	}

	return responses{res, qrPhoto}, nil
}

func (b *Bot) handlePaymentProof(ctx context.Context, chatID int64, msgID int, user *storage.User) (responses, error) {
	log.Printf("handlePaymentProof called for user %s (ID: %d, chat_id: %d)", user.Username, user.ID, chatID)
	
	// First, check if there's a payment already in pending_review
	pendingPayments, err := b.repo.GetPaymentsByUserIDAndStatus(ctx, user.ID, storage.PaymentStatusPendingReview)
	if err == nil && len(pendingPayments) > 0 {
		// Payment already in review
		pendingPayment := pendingPayments[len(pendingPayments)-1]
		text := fmt.Sprintf("⏳ Ваша заявка уже на проверке!\n\n"+
			"Код заявки: `%s`\n"+
			"Сумма: %.2f руб.\n"+
			"Срок: %d дней\n"+
			"Устройств: %d\n\n"+
			"Администратор проверит ваш платеж и одобрит его.\n"+
			"После одобрения вы получите уведомление.",
			pendingPayment.ReferenceCode,
			float64(pendingPayment.Amount)/100.0,
			pendingPayment.DurationDays,
			pendingPayment.DeviceCount)
		res := tgbotapi.NewEditMessageText(chatID, msgID, text)
		res.ParseMode = "Markdown"
		res.ReplyMarkup = &mainMenuKeyboard
		return responses{res}, nil
	}

	// Find latest payment with status "created" for this user
	payments, err := b.repo.GetPaymentsByUserIDAndStatus(ctx, user.ID, storage.PaymentStatusCreated)
	if err != nil {
		return responses{errorMessage(chatID, msgID, true)}, err
	}

	var pendingPayment *storage.Payment
	if len(payments) > 0 {
		// Get the most recent payment with status "created"
		pendingPayment = payments[len(payments)-1]
	}

	if pendingPayment == nil {
		text := "❌ Не найдена ожидающая оплата.\n\n" +
			"Создайте заявку через 'Оплата/Продление' в меню."
		res := tgbotapi.NewEditMessageText(chatID, msgID, text)
		res.ReplyMarkup = &mainMenuKeyboard
		return responses{res}, nil
	}

	// Move payment to pending_review status (simplified - no proof required at this step)
	// Proof will be checked by admin
	if err := b.repo.UpdatePaymentStatus(ctx, pendingPayment.ID, storage.PaymentStatusPendingReview, nil); err != nil {
		log.Printf("ERROR: failed to update payment status to pending_review: %v", err)
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to update payment status")
	}
	log.Printf("Payment %d moved to pending_review status", pendingPayment.ID)

	// Notify admin about new payment
	log.Printf("Calling notifyAdminAboutPayment for payment %d, user %s", pendingPayment.ID, user.Username)
	b.notifyAdminAboutPayment(ctx, pendingPayment, user.Username)

	text := fmt.Sprintf("✅ Заявка отправлена на проверку!\n\n"+
		"📋 Ваша заявка:\n"+
		"• Код заявки: `%s`\n"+
		"• Сумма: %.2f руб.\n"+
		"• Срок: %d дней\n"+
		"• Устройств: %d\n\n"+
		"⏳ ОЖИДАЕТ ПРОВЕРКИ АДМИНИСТРАТОРОМ\n\n"+
		"После одобрения вы получите уведомление и VPN конфигурацию.",
		pendingPayment.ReferenceCode,
		float64(pendingPayment.Amount)/100.0,
		pendingPayment.DurationDays,
		pendingPayment.DeviceCount)

	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ParseMode = "Markdown"
	res.ReplyMarkup = &mainMenuKeyboard

	return responses{res}, nil
}

// notifyAdminAboutPayment sends notification to all admins about new payment
func (b *Bot) notifyAdminAboutPayment(ctx context.Context, payment *storage.Payment, username string) {
	log.Printf("notifyAdminAboutPayment called for payment %d, username %s", payment.ID, username)
	adminChatIDs := b.getAdminChatIDs()
	log.Printf("Found %d admin chat IDs: %v", len(adminChatIDs), adminChatIDs)
	if len(adminChatIDs) == 0 {
		log.Printf("WARNING: No admin chat IDs registered, cannot send notification. Admin must send /start first.")
		return
	}

	paymentUser, err := b.repo.GetUserByID(ctx, payment.UserID)
	if err == nil && paymentUser != nil {
		username = paymentUser.Username
	}

	text := fmt.Sprintf("💳 НОВАЯ ОПЛАТА\n\n"+
		"👤 Пользователь: @%s\n"+
		"📆 Срок: %d дней\n"+
		"📱 Устройств: %d\n"+
		"💰 Сумма: %.2f ₽\n\n"+
		"🔑 Код заявки:\n`%s`",
		username,
		payment.DurationDays,
		payment.DeviceCount,
		float64(payment.Amount)/100.0,
		payment.ReferenceCode)

	// Create keyboard with approve/reject buttons
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Подтвердить", fmt.Sprintf("admin_approve:%d", payment.ID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отклонить", fmt.Sprintf("admin_reject:%d", payment.ID)),
		),
	)

	// Send to all registered admin chat IDs
	for _, chatID := range adminChatIDs {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = &keyboard
		if err := b.send(msg); err != nil {
			log.Printf("failed to notify admin (chat_id: %d): %v", chatID, err)
		} else {
			log.Printf("Notification sent to admin (chat_id: %d)", chatID)
		}
	}
}

func (b *Bot) handleAdminCallback(ctx context.Context, chatID int64, msgID int, user *storage.User, data string) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	if data == "admin:pending" {
		return b.handleAdminPendingPayments(ctx, chatID, msgID, user)
	}

	return nil, nil
}

func (b *Bot) handleAdminPendingPayments(ctx context.Context, chatID int64, msgID int, user *storage.User) (responses, error) {
	payments, err := b.billing.GetPendingPayments(ctx)
	if err != nil {
		return responses{errorMessage(chatID, msgID, true)}, err
	}

	if len(payments) == 0 {
		text := "✅ Нет ожидающих оплат."
		res := tgbotapi.NewEditMessageText(chatID, msgID, text)
		res.ReplyMarkup = &adminKeyboard
		return responses{res}, nil
	}

	// Show list of payments
	var buttons [][]tgbotapi.InlineKeyboardButton
	for _, p := range payments {
		paymentUser, err := b.repo.GetUserByID(ctx, p.UserID)
		username := "Unknown"
		if err == nil && paymentUser != nil {
			username = paymentUser.Username
		}

		label := fmt.Sprintf("💰 %s - %d дней, %d устр. - %.2f руб.", username, p.DurationDays, p.DeviceCount, float64(p.Amount)/100.0)
		button := tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("payment_detail:%d", p.ID))
		buttons = append(buttons, []tgbotapi.InlineKeyboardButton{button})
	}

	buttons = append(buttons, []tgbotapi.InlineKeyboardButton{goToMenuButton})

	text := fmt.Sprintf("📋 Ожидающие оплаты (%d):", len(payments))
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: buttons}

	return responses{res}, nil
}

func (b *Bot) handlePaymentDetail(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	payment, err := b.repo.GetPaymentByID(ctx, paymentID)
	if err != nil || payment == nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("payment not found")
	}

	paymentUser, _ := b.repo.GetUserByID(ctx, payment.UserID)
	username := "Unknown"
	if paymentUser != nil {
		username = paymentUser.Username
	}

	text := fmt.Sprintf("📋 Детали оплаты:\n\n"+
		"ID: %d\n"+
		"Пользователь: @%s\n"+
		"Срок: %d дней\n"+
		"Устройств: %d\n"+
		"Сумма: %.2f руб.\n"+
		"Код заявки: `%s`\n\n"+
		"⚠️ КОММЕНТАРИЙ К ПЕРЕВОДУ:\n"+
		"`%s`\n\n"+
		"При одобрении проверьте:\n"+
		"✅ Сумма платежа\n"+
		"✅ Комментарий к переводу\n"+
		"✅ Скриншот подтверждения\n\n"+
		"Статус: %s\n"+
		"Создано: %s",
		payment.ID, username, payment.DurationDays, payment.DeviceCount,
		float64(payment.Amount)/100.0, payment.ReferenceCode,
		payment.PaymentComment,
		payment.Status, payment.CreatedAt.Format("02.01.2006 15:04"))

	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ParseMode = "Markdown"

	buttons := [][]tgbotapi.InlineKeyboardButton{
		{
			tgbotapi.NewInlineKeyboardButtonData("✅ Проверить и одобрить", fmt.Sprintf("approve_verify:%d", payment.ID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отклонить", fmt.Sprintf("reject:%d", payment.ID)),
		},
		{goToMenuButton},
	}

	if payment.ProofFileID != "" {
		// Send proof photo
		photoMsg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(payment.ProofFileID))
		photoMsg.Caption = "Подтверждение оплаты"
		res.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: buttons}
		return responses{photoMsg, res}, nil
	}

	res.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: buttons}
	return responses{res}, nil
}

func (b *Bot) handleApprovePaymentVerify(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	payment, err := b.repo.GetPaymentByID(ctx, paymentID)
	if err != nil || payment == nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("payment not found")
	}

	text := fmt.Sprintf("✅ Проверьте платеж и введите комментарий к переводу:\n\n"+
		"Ожидаемый комментарий: `%s`\n\n"+
		"Введите комментарий, который указан в скриншоте платежа.\n"+
		"Если комментарий совпадает, платеж будет одобрен.\n\n"+
		"Отправьте комментарий в следующем сообщении.",
		payment.PaymentComment)

	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ParseMode = "Markdown"

	buttons := [][]tgbotapi.InlineKeyboardButton{
		{
			tgbotapi.NewInlineKeyboardButtonData("✅ Одобрить с этим комментарием", fmt.Sprintf("approve:%d:%s", paymentID, payment.PaymentComment)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отклонить", fmt.Sprintf("reject:%d", paymentID)),
		},
		{goToMenuButton},
	}
	res.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: buttons}

	return responses{res}, nil
}

func (b *Bot) handleApprovePayment(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64, verifiedComment string) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	// If comment is not provided, use payment's comment (simplified flow)
	if verifiedComment == "" {
		payment, err := b.repo.GetPaymentByID(ctx, paymentID)
		if err != nil || payment == nil {
			return responses{errorMessage(chatID, msgID, true)}, errors.New("payment not found")
		}
		verifiedComment = payment.PaymentComment
	}

	// Get payment before approval to get user info
	payment, err := b.repo.GetPaymentByID(ctx, paymentID)
	if err != nil || payment == nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("payment not found")
	}

	// Verify and approve payment
	if err := b.billing.AdminApprovePayment(ctx, paymentID, user.Username, verifiedComment); err != nil {
		// If verification fails, show error
		errMsg := fmt.Sprintf("❌ Ошибка при одобрении:\n\n%s\n\nПроверьте комментарий к переводу.", err.Error())
		res := tgbotapi.NewEditMessageText(chatID, msgID, errMsg)
		res.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{
			InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
				{tgbotapi.NewInlineKeyboardButtonData("🔄 Попробовать снова", fmt.Sprintf("approve_verify:%d", paymentID))},
				{goToMenuButton},
			},
		}
		return responses{res}, nil
	}

	// Get updated payment and user after approval
	paymentUser, _ := b.repo.GetUserByID(ctx, payment.UserID)
	
	text := fmt.Sprintf("✅ Платеж одобрен!\n\nПодписка активирована.")
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ReplyMarkup = &adminKeyboard

	// Automatically create device and send config to user
	if paymentUser != nil {
		// Get active subscription (created by AdminApprovePayment)
		subscription, err := b.repo.GetActiveSubscriptionByUserID(ctx, payment.UserID)
		if err == nil && subscription != nil {
			// Create device automatically
			deviceCount, _ := b.repo.CountActiveDevicesBySubscription(ctx, subscription.ID)
			deviceName := fmt.Sprintf("device_%d", deviceCount+1)
			
			// Create WireGuard config
			cfg, _, assignedIP, err := b.wireguard.CreateConfigForNewKeys(ctx, payment.UserID, subscription.ID, deviceName)
			if err == nil {
				content, err := io.ReadAll(cfg)
				if err == nil {
					// Send notification with config
					notifyText := fmt.Sprintf("✅ Ваш платеж одобрен!\n\n"+
						"Подписка активирована на %d дней.\n"+
						"Устройств: %d\n\n"+
						"📱 Ваш WireGuard конфиг готов!\n"+
						"IP адрес: %s\n\n"+
						"Используйте QR-код для подключения на телефоне или скачайте .conf файл для ПК.",
						payment.DurationDays, payment.DeviceCount, assignedIP)
					
					msg := tgbotapi.NewMessage(paymentUser.TelegramID, notifyText)
					file := createFile(paymentUser.TelegramID, content)
					qr := createQR(paymentUser.TelegramID, content)
					
					// Send messages
					b.send(msg)
					if qr != nil {
						b.send(qr)
					}
					b.send(file)
				} else {
					log.Printf("failed to read config: %v", err)
					// Fallback notification
					notifyText := fmt.Sprintf("✅ Ваш платеж одобрен!\n\n"+
						"Подписка активирована на %d дней.\n"+
						"Вы можете создать устройства через /newkeys",
						payment.DurationDays)
					b.SendNotification(paymentUser.TelegramID, notifyText)
				}
			} else {
				log.Printf("failed to create device: %v", err)
				// Fallback notification
				notifyText := fmt.Sprintf("✅ Ваш платеж одобрен!\n\n"+
					"Подписка активирована на %d дней.\n"+
					"Вы можете создать устройства через /newkeys",
					payment.DurationDays)
				b.SendNotification(paymentUser.TelegramID, notifyText)
			}
		} else {
			// Fallback notification if subscription not found
			notifyText := fmt.Sprintf("✅ Ваш платеж одобрен!\n\n"+
				"Подписка активирована на %d дней.\n"+
				"Вы можете создать устройства через /newkeys",
				payment.DurationDays)
			b.SendNotification(paymentUser.TelegramID, notifyText)
		}
	}

	return responses{res}, nil
}

// handleAdminApprovePayment - simplified admin approval (from notification)
func (b *Bot) handleAdminApprovePayment(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	// Get payment
	payment, err := b.repo.GetPaymentByID(ctx, paymentID)
	if err != nil || payment == nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("payment not found")
	}

	// Approve payment (use payment's comment as verified)
	if err := b.billing.AdminApprovePayment(ctx, paymentID, user.Username, payment.PaymentComment); err != nil {
		errMsg := fmt.Sprintf("❌ Ошибка при одобрении:\n\n%s", err.Error())
		res := tgbotapi.NewEditMessageText(chatID, msgID, errMsg)
		return responses{res}, nil
	}

	// Update message
	text := "✅ Платеж одобрен!\n\nПодписка активирована."
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)

	// Get user and send VPN config
	paymentUser, _ := b.repo.GetUserByID(ctx, payment.UserID)
	if paymentUser != nil {
		// Get active subscription
		subscription, err := b.repo.GetActiveSubscriptionByUserID(ctx, payment.UserID)
		if err == nil && subscription != nil {
			// Create device automatically
			deviceCount, _ := b.repo.CountActiveDevicesBySubscription(ctx, subscription.ID)
			deviceName := fmt.Sprintf("device_%d", deviceCount+1)
			
			// Create WireGuard config
			cfg, _, assignedIP, err := b.wireguard.CreateConfigForNewKeys(ctx, payment.UserID, subscription.ID, deviceName)
			if err == nil {
				content, err := io.ReadAll(cfg)
				if err == nil {
					// Send notification with config
					notifyText := fmt.Sprintf("✅ Ваш платеж одобрен!\n\n"+
						"Подписка активирована на %d дней.\n"+
						"Устройств: %d\n\n"+
						"📱 Ваш WireGuard конфиг готов!\n"+
						"IP адрес: %s\n\n"+
						"Используйте QR-код для подключения на телефоне или скачайте .conf файл для ПК.",
						payment.DurationDays, payment.DeviceCount, assignedIP)
					
					msg := tgbotapi.NewMessage(paymentUser.TelegramID, notifyText)
					file := createFile(paymentUser.TelegramID, content)
					qr := createQR(paymentUser.TelegramID, content)
					
					// Send messages
					b.send(msg)
					if qr != nil {
						b.send(qr)
					}
					b.send(file)
					log.Printf("VPN config sent to user %d", paymentUser.TelegramID)
				} else {
					log.Printf("failed to read config: %v", err)
				}
			} else {
				log.Printf("failed to create device: %v", err)
			}
		}
	}

	return responses{res}, nil
}

// handleAdminRejectPayment - simplified admin rejection (from notification)
func (b *Bot) handleAdminRejectPayment(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	if err := b.billing.AdminRejectPayment(ctx, paymentID, user.Username); err != nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to reject payment")
	}

	payment, _ := b.repo.GetPaymentByID(ctx, paymentID)
	paymentUser, _ := b.repo.GetUserByID(ctx, payment.UserID)

	text := "❌ Платеж отклонен."
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)

	// Notify user
	if paymentUser != nil {
		notifyText := "❌ Ваш платеж отклонен администратором.\n\nОбратитесь в поддержку для уточнения деталей."
		b.SendNotification(paymentUser.TelegramID, notifyText)
	}

	return responses{res}, nil
}

func (b *Bot) handleRejectPayment(ctx context.Context, chatID int64, msgID int, user *storage.User, paymentID int64) (responses, error) {
	if !b.isAdmin(user.Username) {
		return responses{errorMessage(chatID, msgID, true)}, errors.New("not an admin")
	}

	if err := b.billing.AdminRejectPayment(ctx, paymentID, user.Username); err != nil {
		return responses{errorMessage(chatID, msgID, true)}, errors.Wrap(err, "failed to reject payment")
	}

	payment, _ := b.repo.GetPaymentByID(ctx, paymentID)
	paymentUser, _ := b.repo.GetUserByID(ctx, payment.UserID)

	text := fmt.Sprintf("❌ Платеж отклонен.")
	res := tgbotapi.NewEditMessageText(chatID, msgID, text)
	res.ReplyMarkup = &adminKeyboard

	// Notify user
	if paymentUser != nil {
		notifyText := "❌ Ваш платеж отклонен администратором.\n\nОбратитесь в поддержку для уточнения деталей."
		b.SendNotification(paymentUser.TelegramID, notifyText)
	}

	return responses{res}, nil
}

func (b *Bot) handleConfigForNewKeys(chatID int64, userID int64, username string, _ string) (responses, error) {
	ctx := context.Background()

	// Check access
	result, err := b.access.CanProvisionDevice(ctx, userID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check access")
	}

	if !result.CanProvision {
		msg := tgbotapi.NewMessage(chatID, result.Reason)
		msg.ReplyMarkup = &mainMenuKeyboard
		return responses{msg}, nil
	}

	// Get active subscription
	subscription, err := b.repo.GetActiveSubscriptionByUserID(ctx, userID)
	if err != nil || subscription == nil {
		return nil, errors.New("subscription not found")
	}

	// Generate device name
	deviceCount, _ := b.repo.CountActiveDevicesBySubscription(ctx, subscription.ID)
	deviceName := fmt.Sprintf("device_%d", deviceCount+1)

	// Create config
	cfg, _, _, err := b.wireguard.CreateConfigForNewKeys(ctx, userID, subscription.ID, deviceName)
	if err != nil {
		return responses{errorMessage(chatID, 0, false)}, errors.Wrap(err, "failed to create new config")
	}

	content, err := io.ReadAll(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read new config")
	}

	msg := tgbotapi.NewMessage(chatID, emoji())
	file := createFile(chatID, content)
	qr := createQR(chatID, content)

	if qr == nil {
		return responses{msg, file}, nil
	}
	return responses{msg, qr, file}, nil
}

func createFile(chatID int64, content []byte) tgbotapi.Chattable {
	name := strconv.FormatInt(time.Now().Unix(), 10)
	return tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{
		Name:  name + ".conf",
		Bytes: content,
	})
}

func createQR(chatID int64, content []byte) tgbotapi.Chattable {
	options := []qrcode.ImageOption{
		qrcode.WithLogoImageFilePNG("assets/logo-min.png"),
		qrcode.WithQRWidth(7),
		qrcode.WithBuiltinImageEncoder(qrcode.PNG_FORMAT),
	}
	qrc, err := qrcode.New(string(content), options...)
	if err != nil {
		log.Printf("failed to create qr code: %v", err)
		return nil
	}
	buf := bytes.Buffer{}
	if err := qrc.SaveTo(&buf); err != nil {
		log.Printf("failed to read new qr code: %v", err)
		return nil
	}
	name := strconv.FormatInt(time.Now().Unix(), 10)
	return tgbotapi.NewPhoto(chatID, tgbotapi.FileReader{
		Name:   name + ".png",
		Reader: &buf,
	})
}

// sendPaymentQR sends the static payment QR code from file
func (b *Bot) sendPaymentQR(chatID int64) tgbotapi.Chattable {
	if b.paymentQRPath == "" {
		log.Printf("PAYMENT_QR_PATH is not set, cannot send QR code")
		return nil
	}
	
	// Read file content into bytes
	fileBytes, err := os.ReadFile(b.paymentQRPath)
	if err != nil {
		log.Printf("failed to read payment QR file '%s': %v", b.paymentQRPath, err)
		return nil
	}
	
	if len(fileBytes) == 0 {
		log.Printf("payment QR file '%s' is empty", b.paymentQRPath)
		return nil
	}
	
	// Get file name from path
	fileName := filepath.Base(b.paymentQRPath)
	if fileName == "" || fileName == "." {
		fileName = "payment_qr.png"
	}
	
	// Send photo from file bytes
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{
		Name:  fileName,
		Bytes: fileBytes,
	})
	photo.Caption = "QR-код для оплаты"
	return photo
}

func init() {
	ConfigForNewKeysCmd.handler = (*Bot).handleConfigForNewKeys
	StartCmd.handler = func(b *Bot, chatID int64, userID int64, username string, arg string) (responses, error) {
		return nil, nil
	}
	MenuCmd.handler = func(b *Bot, chatID int64, userID int64, username string, arg string) (responses, error) {
		return nil, nil
	}
	AdminCmd.handler = func(b *Bot, chatID int64, userID int64, username string, arg string) (responses, error) {
		if !b.isAdmin(username) {
			return responses{tgbotapi.NewMessage(chatID, "❌ У вас нет прав администратора.")}, nil
		}
		text := "👑 Админ-панель"
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ReplyMarkup = &adminKeyboard
		return responses{msg}, nil
	}
}

const sorry = "Что-то пошло не так, извините 👉🏻👈🏻"

func errorMessage(chatID int64, msgID int, edit bool) (res tgbotapi.Chattable) {
	if edit {
		res = tgbotapi.NewEditMessageTextAndMarkup(
			chatID, msgID, sorry,
			tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(goToMenuButton),
			),
		)
	} else {
		res = tgbotapi.NewMessage(chatID, sorry)
	}
	return
}
