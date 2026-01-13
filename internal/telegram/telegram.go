package telegram

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/access"
	"github.com/skoret/wireguard-bot/internal/billing"
	"github.com/skoret/wireguard-bot/internal/storage"
	"github.com/skoret/wireguard-bot/internal/wireguard"
)

type Bot struct {
	wg            *sync.WaitGroup
	api           *tgbotapi.BotAPI
	wireguard     wireguard.Wireguard
	admins        map[string]struct{}      // Admin usernames
	adminChatIDs  map[string]int64         // Admin username -> chat_id mapping
	adminMutex    sync.RWMutex             // Mutex for adminChatIDs access
	repo          *storage.Repository
	billing       *billing.Service
	access        *access.Service
	paymentQRPath string // Path to static payment QR code image
}

// NewBot creates new Bot instance
func NewBot(token string, repo *storage.Repository, billingService *billing.Service, accessService *access.Service, paymentQRPath string) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	log.Printf("bot user: %+v", api.Self)

	// Create WireGuard instance - it will automatically choose between LocalProvisioner (Stage 1)
	// and DevProvisioner based on DEV_MODE environment variable
	wguard, err := wireguard.NewWireguard(repo)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create wireguard client")
	}

	var admins map[string]struct{}
	if usernames := os.Getenv("ADMIN_USERNAMES"); len(usernames) != 0 {
		users := strings.Split(usernames, ",")
		admins = make(map[string]struct{}, len(users))
		for _, user := range users {
			admins[strings.TrimSpace(user)] = struct{}{}
		}
	}

	bot := &Bot{
		wg:            &sync.WaitGroup{},
		api:           api,
		wireguard:     wguard,
		admins:        admins,
		adminChatIDs:  make(map[string]int64),
		repo:          repo,
		billing:       billingService,
		access:        accessService,
		paymentQRPath: paymentQRPath,
	}

	if err := bot.setMyCommands(); err != nil {
		return nil, err
	}

	return bot, nil
}

// SendNotification sends a notification message to a user
func (b *Bot) SendNotification(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := b.api.Send(msg)
	return err
}

func (b *Bot) Run(ctx context.Context) error {
	// wait all running handlers to finish and close wg connection
	defer func() {
		b.wg.Wait()
		if err := b.wireguard.Close(); err != nil {
			log.Printf("failed to close wireguard connection: %v", err)
		}
	}()

	config := tgbotapi.NewUpdate(0)
	config.Timeout = 30

	// Start polling Telegram for updates
	// TODO: someday it should be webhook instead of updates pulling
	updates := b.api.GetUpdatesChan(config)

	for {
		select {
		case update := <-updates:
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()
				if errs := b.handle(&update); errs != nil {
					for _, err := range errs {
						log.Printf("error occured: %s", err.Error())
					}
				}
			}()
		case <-ctx.Done():
			log.Printf("stopping bot: %v", ctx.Err())
			b.api.StopReceivingUpdates()
			return nil
		}
	}
}

func (b *Bot) isAdmin(user string) bool {
	if len(b.admins) == 0 {
		return false
	}
	_, ok := b.admins[user]
	return ok
}

func notAdminMsg(chatID int64) []tgbotapi.Chattable {
	return []tgbotapi.Chattable{
		tgbotapi.NewMessage(chatID, "❌ У вас нет прав администратора."),
	}
}

func (b *Bot) handle(update *tgbotapi.Update) []error {
	log.Printf("new update: %+v", update)
	var res []tgbotapi.Chattable
	var err error
	errs := make([]error, 0)
	switch {
	case update.Message != nil:
		msg := update.Message
		// For admin commands, check auth. For regular commands, allow all
		// Admin commands will be handled in handlers
		res, err = b.handleMessage(msg)
	case update.CallbackQuery != nil:
		query := update.CallbackQuery
		res, err = b.handleQuery(query)
	default:
		errs = append(errs, errors.New("unable to handle such update"))
	}
	if err != nil {
		errs = append(errs, err)
	}
	for _, resp := range res {
		if err := b.send(resp); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (b *Bot) send(c tgbotapi.Chattable) error {
	if c == nil {
		return nil
	}
	
	// Check if message has empty text (but allow photos/documents/files)
	switch v := c.(type) {
	case tgbotapi.MessageConfig:
		// Only check text for plain messages
		if v.Text == "" {
			log.Printf("WARNING: Attempted to send empty message (Text is empty), skipping")
			return nil
		}
	case *tgbotapi.MessageConfig:
		if v.Text == "" {
			log.Printf("WARNING: Attempted to send empty message (Text is empty), skipping")
			return nil
		}
	case tgbotapi.EditMessageTextConfig:
		if v.Text == "" {
			log.Printf("WARNING: Attempted to send empty edit message (Text is empty), skipping")
			return nil
		}
	case *tgbotapi.EditMessageTextConfig:
		if v.Text == "" {
			log.Printf("WARNING: Attempted to send empty edit message (Text is empty), skipping")
			return nil
		}
	case tgbotapi.PhotoConfig:
		// Photo messages are OK even without caption
		// But check if file is actually set
		if v.File == nil {
			log.Printf("WARNING: Attempted to send photo without file, skipping")
			return nil
		}
	case *tgbotapi.PhotoConfig:
		if v.File == nil {
			log.Printf("WARNING: Attempted to send photo without file, skipping")
			return nil
		}
	case tgbotapi.DocumentConfig:
		// Document messages are OK
		if v.File == nil {
			log.Printf("WARNING: Attempted to send document without file, skipping")
			return nil
		}
	case *tgbotapi.DocumentConfig:
		if v.File == nil {
			log.Printf("WARNING: Attempted to send document without file, skipping")
			return nil
		}
	}
	
	msg, err := b.api.Send(c)
	if err != nil {
		log.Printf("ERROR sending message: %v", err)
		return err
	}
	log.Printf("send msg: %+v", msg)
	return nil
}

// registerAdmin registers admin chat_id when they send /start
func (b *Bot) registerAdmin(username string, chatID int64) {
	if username == "" {
		return
	}
	b.adminMutex.Lock()
	defer b.adminMutex.Unlock()
	if _, isAdmin := b.admins[username]; isAdmin {
		b.adminChatIDs[username] = chatID
		log.Printf("Admin registered: %s -> chat_id: %d", username, chatID)
	}
}

// getAdminChatIDs returns all admin chat IDs
func (b *Bot) getAdminChatIDs() []int64 {
	b.adminMutex.RLock()
	defer b.adminMutex.RUnlock()
	chatIDs := make([]int64, 0, len(b.adminChatIDs))
	for _, chatID := range b.adminChatIDs {
		chatIDs = append(chatIDs, chatID)
	}
	return chatIDs
}
