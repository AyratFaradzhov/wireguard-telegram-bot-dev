# Production Deployment Guide

## Pre-flight Checks

Перед запуском бота в production убедитесь, что выполнены следующие проверки:

### 1. WireGuard Interface

```bash
# Проверка, что интерфейс существует и активен
sudo wg show

# Если интерфейс не существует, создайте его
sudo wg-quick up wg0
```

### 2. Environment Variables

Создайте `.env` файл на основе `.env.example` и убедитесь, что все обязательные переменные установлены:

```bash
# Обязательные переменные
TELEGRAM_APITOKEN=your_bot_token_here
WIREGUARD_INTERFACE=wg0
SERVER_ENDPOINT=your_server_ip:51820
DNS_IPS=8.8.8.8,8.8.4.4
STATIC_QR_CODE=your_static_qr_code

# Опциональные
DATABASE_DSN=/var/lib/wireguard-bot/bot.db
ADMIN_USERNAMES=admin1,admin2
```

### 3. Database Permissions

```bash
# Создайте директорию для БД
sudo mkdir -p /var/lib/wireguard-bot
sudo chown $USER:$USER /var/lib/wireguard-bot

# Или используйте текущую директорию (для тестирования)
# Убедитесь, что директория доступна для записи
```

### 4. Build & Run

```bash
# Сборка проекта
go build -o wireguard-bot cmd/bot/main.go

# Запуск с root правами (необходимо для управления WireGuard)
sudo ./wireguard-bot
```

## Validation on Startup

Бот автоматически проверяет:

1. ✅ **Обязательные переменные окружения:**
   - `TELEGRAM_APITOKEN` - токен бота
   - `WIREGUARD_INTERFACE` - имя интерфейса (например, `wg0`)
   - `SERVER_ENDPOINT` - внешний IP:порт сервера
   - `DNS_IPS` - DNS серверы через запятую
   - `STATIC_QR_CODE` - статический QR-код для оплаты

2. ✅ **WireGuard интерфейс:**
   - Интерфейс существует в системе
   - Интерфейс доступен через `wgctrl`

3. ✅ **DNS IP адреса:**
   - Валидные IP адреса
   - Хотя бы один DNS сервер указан

4. ✅ **База данных:**
   - Директория существует и доступна для записи
   - SQLite файл может быть создан/открыт

## Error Messages

### "TELEGRAM_APITOKEN environment variable is required"
**Решение:** Установите переменную `TELEGRAM_APITOKEN` в `.env` или экспортируйте в окружении.

### "WireGuard interface 'wg0' not found"
**Решение:** 
- Проверьте, что интерфейс существует: `sudo wg show`
- Если нет, создайте: `sudo wg-quick up wg0`
- Или измените `WIREGUARD_INTERFACE` на существующий интерфейс

### "database directory not writable"
**Решение:**
- Убедитесь, что директория БД существует
- Проверьте права доступа: `ls -la /path/to/db/directory`
- Создайте директорию и установите права: `sudo mkdir -p /var/lib/wireguard-bot && sudo chown $USER:$USER /var/lib/wireguard-bot`

### "invalid DNS IP address"
**Решение:** Проверьте формат `DNS_IPS` - должны быть валидные IP адреса через запятую: `8.8.8.8,8.8.4.4`

### "failed to create wgctrl client"
**Решение:** 
- Убедитесь, что запускаете с правами root: `sudo ./wireguard-bot`
- Проверьте, что WireGuard модуль загружен: `lsmod | grep wireguard`

## Systemd Service (Optional)

Создайте systemd unit для автоматического запуска:

```ini
[Unit]
Description=WireGuard Telegram Bot
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/wireguard-bot
EnvironmentFile=/opt/wireguard-bot/.env
ExecStart=/opt/wireguard-bot/wireguard-bot
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```bash
# Установка сервиса
sudo cp wireguard-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable wireguard-bot
sudo systemctl start wireguard-bot

# Проверка статуса
sudo systemctl status wireguard-bot
```

## Logs

Логи выводятся в stdout/stderr. Для сохранения логов:

```bash
# Запуск с логированием в файл
sudo ./wireguard-bot 2>&1 | tee /var/log/wireguard-bot.log

# Или через systemd journal
sudo journalctl -u wireguard-bot -f
```

## Monitoring

Проверка работоспособности бота:

```bash
# Проверка WireGuard peers
sudo wg show wg0

# Проверка базы данных
sqlite3 /var/lib/wireguard-bot/bot.db "SELECT COUNT(*) FROM subscriptions WHERE status='active';"

# Проверка активных устройств
sqlite3 /var/lib/wireguard-bot/bot.db "SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL;"
```


