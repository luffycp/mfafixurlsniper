
// luffy & jojo

package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	setConsoleTitle = kernel32.NewProc("SetConsoleTitleW")
)

func setTitle(title string) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	setConsoleTitle.Call(uintptr(unsafe.Pointer(titlePtr)))
}

type Config struct {
	Token        string `json:"token"`
	Password     string `json:"password"`
	GuildID      string `json:"guild_id"`
	NewVanityURL string `json:"new_vanity_url"`
	License      string `json:"License"`
	WebhookURL   string `json:"webhook_url"`
}

type MFAPayload struct {
	Ticket string `json:"ticket"`
	Type   string `json:"mfa_type"`
	Data   string `json:"data"`
}

type MFAResponse struct {
	Token string `json:"token"`
}

type VanityResponse struct {
	MFA struct {
		Ticket string `json:"ticket"`
	} `json:"mfa"`
}

type GatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int             `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type IdentifyPayload struct {
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	Properties map[string]string `json:"properties"`
}

type ReadyEvent struct {
	V      int     `json:"v"`
	User   User    `json:"user"`
	Guilds []Guild `json:"guilds"`
}

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Discriminator string `json:"discriminator"`
}

type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type GuildUpdateEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type GuildDeleteEvent struct {
	ID string `json:"id"`
}

var (
	socket        *websocket.Conn
	mu            sync.Mutex
	sequence      int
	reconnectChan = make(chan struct{})
	mfaToken      string 
	mfaRetryCount int    
	maxMfaRetries = 3    
	guilds        = make(map[string]string)
	config        Config
	webhookURL    string

	fastHttpClient = &fasthttp.Client{
		TLSConfig:       &tls.Config{InsecureSkipVerify: true}, 
		MaxConnsPerHost: 1000,                                  
	}
)

const (
	DiscordGatewayURL    = "wss://gateway.discord.gg/?v=10&encoding=json"
	OpcodeDispatch       = 0
	OpcodeHeartbeat      = 1
	OpcodeIdentify       = 2
	OpcodeReconnect      = 7
	OpcodeInvalidSession = 9
	OpcodeHello          = 10
	OpcodeHeartbeatAck   = 11
	Intents              = 1 << 0
)

type CustomFormatter struct{}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("15:04:05.000")
	level := strings.ToUpper(entry.Level.String())

	subsystem, ok := entry.Data["subsystem"].(string)
	if !ok || subsystem == "" {
		subsystem = "General"
	}

	logMessage := fmt.Sprintf("%s [%s/%s] %s\n", timestamp, level, subsystem, entry.Message)
	return []byte(logMessage), nil
}

func initLogger() {

	logrus.SetFormatter(&CustomFormatter{})
	logrus.SetLevel(logrus.InfoLevel)
	logrus.SetOutput(os.Stdout)

}

func logInfo(subsystem, message string) {
	logrus.WithField("subsystem", subsystem).Info(message)
}

func logError(subsystem, message string) {
	logrus.WithField("subsystem", subsystem).Error(message)
}

func logSuccess(subsystem, message string) {
	logrus.WithField("subsystem", subsystem).Info("SUCCESS: " + message)
}

func clearConsole() {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "cls") // Windows
	default:
		cmd = exec.Command("clear")
	}
	cmd.Stdout = os.Stdout
	cmd.Run()
}

func Input(message string) string {
	fmt.Print(message)
	var input string
	fmt.Scanln(&input)
	return input
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

func setCommonHeaders(req *fasthttp.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) discord/1.0.9164 "+
		"Chrome/124.0.6367.243 Electron/30.2.0 Safari/537.36")
	req.Header.Set("X-Super-Properties", "eyJvcyI6IldpbmRvd3MiLCJicm93c2VyIjoiRGlzY29yZCBDbGllbnQiLCJyZWxlYXNlX2NoYW5uZWwiOiJzdGFibGUiLCJjbGllbnRfdmVyc2lvbiI6IjEuMC45MTY0Iiwib3NfdmVyc2lvbiI6IjEwLjAuMjI2MzEiLCJvc19hcmNoIjoieDY0IiwiYXBwX2FyY2giOiJ4NjQiLCJzeXN0ZW1fbG9jYWxlIjoidHIiLCJicm93c2VyX3VzZXJfYWdlbnQiOiJNb3ppbGxhLzUuMCAoV2luZG93cyBOVCAxMC4wOyBXaW42NDsgeDY0KSBBcHBsZVdlYktpdC81MzcuMzYgKEtIVE1MLCBsaWtlIEdlY2tvKSBkaXNjb3JkLzEuMC45MTY0IENocm9tZS8xMjQuMC42MzY3LjI0MyBFbGVjdHJvbi8zMC4yLjAgU2FmYXJpLzUzNy4zNiIsImJyb3dzZXJfdmVyc2lvbiI6IjMwLjIuMCIsIm9zX3Nka192ZXJzaW9uIjoiMjI2MzEiLCJjbGllbnRfdnVibF9udW1iZXIiOjUyODI2LCJjbGllbnRfZXZlbnRfc291cmNlIjpudWxsfQ==")
	req.Header.Set("X-Discord-Timezone", "Europe/Istanbul")
	req.Header.Set("X-Discord-Locale", "en-US")
	req.Header.Set("X-Debug-Options", "bugReporterEnabled")
	req.Header.Set("Content-Type", "application/json")
}

func sendWebhook(message string) error {
	if webhookURL == "" {
		return fmt.Errorf("webhook URL is not configured")
	}

	payload := map[string]string{
		"content": message,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(webhookURL)
	req.Header.SetMethod("POST")
	setCommonHeaders(req, "") 

	req.SetBody(jsonData)

	err = fastHttpClient.Do(req, resp)
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}

	statusCode := resp.StatusCode()
	if statusCode < 200 || statusCode >= 300 {
		bodyBytes := resp.Body()
		return fmt.Errorf("webhook responded with status code %d: %s", statusCode, string(bodyBytes))
	}

	return nil
}

func connectGateway() error {
	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	var err error
	socket, _, err = dialer.Dial(DiscordGatewayURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to gateway: %w", err)
	}
	logInfo("Connection", "Connected to Discord Gateway.")
	return nil
}

func identifyGateway(token string) error {
	identify := IdentifyPayload{
		Token:   token,
		Intents: Intents,
		Properties: map[string]string{
			"$os":      "linux",
			"$browser": "FireFox",
			"$device":  ".gg/900",
		},
	}

	payloadData, err := json.Marshal(identify)
	if err != nil {
		return fmt.Errorf("failed to marshal identify payload: %w", err)
	}

	gatewayPayload := GatewayPayload{
		Op: OpcodeIdentify,
		D:  payloadData,
	}

	if err := socket.WriteJSON(gatewayPayload); err != nil {
		return fmt.Errorf("failed to send identify payload: %w", err)
	}

	logInfo("Gateway", "Identify payload sent.")
	return nil
}

func handleMessages(token, guildID, newURL, pass string) {
	for {
		_, message, err := socket.ReadMessage()
		if err != nil {
			logError("Gateway", fmt.Sprintf("Error reading message from WebSocket: %v", err))
			reconnectChan <- struct{}{}
			return
		}

		var payload GatewayPayload
		if err := json.Unmarshal(message, &payload); err != nil {
			logError("Gateway", fmt.Sprintf("Error decoding JSON: %v", err))
			continue
		}
		var data map[string]interface{}
		err = json.Unmarshal(message, &data)
		if err != nil {
			logError("Gateway", fmt.Sprintf("Error decoding JSON: %v", err))
			continue
		}
		eventType, _ := data["t"].(string)
		switch payload.Op {
		case OpcodeDispatch:
			switch eventType {
			case "READY":
				var ready ReadyEvent
				if err := json.Unmarshal(payload.D, &ready); err != nil {
					logError("Gateway", fmt.Sprintf("Error unmarshalling READY event: %v", err))
					continue
				}
				logInfo("Gateway", fmt.Sprintf("Logged in as %s#%s (%s)", ready.User.Username, ready.User.Discriminator, ready.User.ID))

				guildList := data["d"].(map[string]interface{})["guilds"].([]interface{})
				var vanityURLs []string
				for _, guild := range guildList {
					guildMap := guild.(map[string]interface{})
					if vanityURLCode, exists := guildMap["vanity_url_code"].(string); exists {
						guilds[guildMap["id"].(string)] = vanityURLCode
						vanityURLs = append(vanityURLs, vanityURLCode)
					}
				}

				logInfo("Gateway", fmt.Sprintf("Vanity: %v", vanityURLs))

			case "GUILD_UPDATE":
				d, ok := data["d"].(map[string]interface{})
				if !ok {
					logError("Gateway", "GUILD_UPDATE event has invalid or missing 'd' field")
					break
				}
				guildIDVal, exists := d["guild_id"]
				if !exists {
					logError("Gateway", "GUILD_UPDATE event missing 'guild_id'")
					break
				}

				guildIDxxd, ok := guildIDVal.(string)
				if !ok {
					logError("Gateway", "GUILD_UPDATE event 'guild_id' is not a string")
					break
				}
				vanityURLVal, exists := d["vanity_url_code"]
				var vanityURL string
				if exists && vanityURLVal != nil {
					vanityURL, ok = vanityURLVal.(string)
					if !ok {
						logError("Gateway", "GUILD_UPDATE event 'vanity_url_code' is not a string")
						break
					}
				} else {
					vanityURL = ""
				}
				guild, ok := guilds[guildIDxxd] 
				if ok && guild != vanityURL {
					go getURL(token, guildID, guild, pass, false)
				}
			}
		case OpcodeHello:
			var hello struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			if err := json.Unmarshal(payload.D, &hello); err != nil {
				logError("Gateway", fmt.Sprintf("Error unmarshalling HELLO payload: %v", err))
				continue
			}
			go startHeartbeat(hello.HeartbeatInterval)
		case OpcodeHeartbeatAck:
			logInfo("Gateway", "Heartbeat acknowledged.")
		case OpcodeReconnect:
			logError("Gateway", "Received RECONNECT opcode, reconnecting...")
			reconnectChan <- struct{}{}
			return
		case OpcodeInvalidSession:
			logError("Gateway", "Received INVALID_SESSION opcode, re-identifying...")
			if err := identifyGateway(token); err != nil {
				logError("Gateway", fmt.Sprintf("Error re-identifying: %v", err))
			}
		default:
		}

		if payload.S != 0 {
			mu.Lock()
			sequence = payload.S
			mu.Unlock()
		}
	}
}
func startHeartbeat(interval int) {
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mu.Lock()
			hbSeq := sequence
			mu.Unlock()

			heartbeatPayload := GatewayPayload{
				Op: OpcodeHeartbeat,
				D:  json.RawMessage(fmt.Sprintf("%d", hbSeq)),
			}

			payloadBytes, err := json.Marshal(heartbeatPayload)
			if err != nil {
				logError("Gateway", fmt.Sprintf("Error marshalling heartbeat payload: %v", err))
				reconnectChan <- struct{}{}
				return
			}

			if err := socket.WriteMessage(websocket.TextMessage, payloadBytes); err != nil {
				logError("Gateway", fmt.Sprintf("Error sending heartbeat: %v", err))
				reconnectChan <- struct{}{}
				return
			}
			logInfo("Gateway", "Heartbeat sent.")
			os.Exit(0)
		}
	}
}
func reconnect(token, guildID, newURL, pass string) {
	for {
		select {
		case <-reconnectChan:
			logInfo("Gateway", "Attempting to reconnect to Discord Gateway...")
			if socket != nil {
				socket.Close()
			}

			time.Sleep(5 * time.Second)

			if err := connectGateway(); err != nil {
				logError("Gateway", fmt.Sprintf("Reconnection failed: %v", err))
				continue
			}

			if err := identifyGateway(token); err != nil {
				logError("Gateway", fmt.Sprintf("Re-identification failed: %v", err))
				continue
			}

			go handleMessages(token, guildID, newURL, pass)
		}
	}
}

func sendMFA(token, ticket, pass string) string {

	logInfo("Checker", "Starting MFA process...")

	payload := MFAPayload{
		Ticket: ticket,
		Type:   "password",
		Data:   pass,
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		logError("Checker", fmt.Sprintf("Error marshalling to JSON: %s", err))
		return "err"
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("https://discord.com/api/v9/mfa/finish")
	req.Header.SetMethod("POST")
	setCommonHeaders(req, token)
	req.SetBody(jsonPayload)

	err = fastHttpClient.Do(req, resp)
	if err != nil {
		logError("Checker", fmt.Sprintf("Network error: %s", err))
		return "err"
	}

	bodyBytes := resp.Body()

	if resp.StatusCode() == fasthttp.StatusOK {
		var mfaResponse MFAResponse
		err := json.Unmarshal(bodyBytes, &mfaResponse)
		if err != nil {
			logError("Checker", fmt.Sprintf("JSON Error: %s - %s - %d", err, string(bodyBytes), resp.StatusCode()))
			return "err"
		}
		logSuccess("Checker", fmt.Sprintf("MFA token received: %s", mfaResponse.Token))
		return mfaResponse.Token
	} else {
		logError("Checker", fmt.Sprintf("Error: %s - %d", string(bodyBytes), resp.StatusCode()))
		if resp.StatusCode() == fasthttp.StatusUnauthorized {
			logError("Checker", "Unauthorized. Check if the MFA ticket or password is correct.")
		}
		return "err"
	}
}
func getURL(token, guildID, newURL, pass string, once bool) {
	startTime := time.Now() // Start time measurement

	body := []byte("{\"code\":\"" + newURL + "\"}")

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	url := "https://canary.discord.com/api/v7/guilds/" + guildID + "/vanity-url"
	req.SetRequestURI(url)
	req.Header.SetMethod("PATCH")
	setCommonHeaders(req, token)
	mu.Lock()
	currentMfaToken := mfaToken
	mu.Unlock()

	if currentMfaToken != "" {
		req.Header.Set("X-Discord-Mfa-Authorization", currentMfaToken)   // Use MFA token
		req.Header.Set("Cookie", "__Secure-recent_mfa="+currentMfaToken) // Use MFA token
	}

	req.SetBody(body)
	err := fastHttpClient.Do(req, resp)
	if err != nil {
		logError("Checker", fmt.Sprintf("Request failed: %v", err))
		return
	}

	elapsed := time.Since(startTime).Seconds() * 1000
	requestTime := fmt.Sprintf("%.1fms", elapsed)

	bodyBytes := resp.Body()

	if resp.StatusCode() != fasthttp.StatusOK {
		if resp.StatusCode() == fasthttp.StatusUnauthorized {
			logError("Checker", "Unauthorized, MFA required.")

			mu.Lock()
			if mfaRetryCount >= maxMfaRetries {
				mu.Unlock()
				logError("Checker", "Maximum MFA attempts reached. Aborting to prevent loop.")
				message := fmt.Sprintf("discord.gg/%s | Failed To Claim | %s", newURL, requestTime)
				if err := sendWebhook(message); err != nil {
					logError("Checker", fmt.Sprintf("Failed to send webhook: %v", err))
				}
				return
			}
			mfaRetryCount++
			mu.Unlock()

			var vanityResponse VanityResponse
			err := json.Unmarshal(bodyBytes, &vanityResponse)
			if err != nil {
				logError("Checker", fmt.Sprintf("Error unmarshalling vanity response: %s", err))
				return
			}
			ticket := vanityResponse.MFA.Ticket
			logInfo("Checker", fmt.Sprintf("MFA Ticket: %s", ticket))
			newMfaToken := sendMFA(token, ticket, pass)
			if newMfaToken == "" || newMfaToken == "err" {
				logError("Checker", "Failed to obtain MFA token.")
				message := fmt.Sprintf("discord.gg/%s | Failed To Claim | %s", newURL, requestTime)
				if err := sendWebhook(message); err != nil {
					logError("Checker", fmt.Sprintf("Failed to send webhook: %v", err))
				}
				return
			}
			mu.Lock()
			mfaToken = newMfaToken
			mu.Unlock()

			logInfo("Checker", "Retrying vanity URL update with new MFA token...")
			getURL(token, guildID, newURL, pass, false)

		} else {
			logError("Checker", fmt.Sprintf("Request failed: %v - %s", err, string(bodyBytes)))
			message := fmt.Sprintf("||@everyone||\n discord.gg/%s | Failed To Claim | %s", newURL, requestTime)
			if err := sendWebhook(message); err != nil {
				logError("Checker", fmt.Sprintf("Failed to send webhook: %v", err))
			}
		}
	} else {
		logSuccess("Claimer", fmt.Sprintf("Claimed vanity: %s", newURL))
		message := fmt.Sprintf("||@everyone||\n discord.gg/%s | Vanity Claimed | 200 |", newURL, )
		if err := sendWebhook(message); err != nil {
			logError("Claimer", fmt.Sprintf("Failed to send webhook: %v", err))
		}
	}
}

func main() {
	initLogger()
	for i := 0; i < 9; i++ {
		logInfo("Configs", "Luffy900, discord.gg/900 ")
	}
	cfg, err := loadConfig("config.json")
	if err != nil {
		logError("Configs", fmt.Sprintf("Error loading configuration: %v", err))
		os.Exit(1)
	}
	config = *cfg
	webhookURL = config.WebhookURL
	if config.Token == "" || config.Password == "" || config.GuildID == "" || config.WebhookURL == "" {
		logError("Configs", "Missing required configuration fields: token, password, guild_id, webhook_url")
		os.Exit(1)
	}
	logInfo("Checker", "Attempting to get MFA ticket...")
	body := []byte("{\"code\":\"" + config.NewVanityURL + "\"}")
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	url := "https://canary.discord.com/api/v7/guilds/" + config.GuildID + "/vanity-url"
	req.SetRequestURI(url)
	req.Header.SetMethod("PATCH")
	setCommonHeaders(req, config.Token)
	req.SetBody(body)

	err = fastHttpClient.Do(req, resp)
	if err != nil {
		logError("Checker", fmt.Sprintf("Failed to get MFA ticket: %v", err))
		os.Exit(1)
	}

	if resp.StatusCode() != fasthttp.StatusUnauthorized {
		bodyBytes := resp.Body()
		logError("Checker", fmt.Sprintf("Failed to get MFA ticket: Expected unauthorized status, got: %d - %s", resp.StatusCode(), string(bodyBytes)))
		os.Exit(1)
	}

	logInfo("Checker", "MFA ticket obtained successfully, processing response...")

	bodyBytes := resp.Body()

	var vanityResponse VanityResponse
	if err := json.Unmarshal(bodyBytes, &vanityResponse); err != nil {
		logError("Checker", fmt.Sprintf("Failed to process response: %s", err))
		os.Exit(1)
	}

	ticket := vanityResponse.MFA.Ticket
	logInfo("Checker", fmt.Sprintf("MFA Ticket: %s", ticket))

	logInfo("Checker", "Starting MFA process...")

	mfaTokenObtained := sendMFA(config.Token, ticket, config.Password)
	if mfaTokenObtained == "" || mfaTokenObtained == "err" {
		logError("Checker", "Failed to obtain MFA token.")
		os.Exit(1)
	} else {
		logSuccess("Checker", fmt.Sprintf("MFA token obtained successfully: %s", mfaTokenObtained))
	}

	mu.Lock()
	mfaToken = mfaTokenObtained
	mfaRetryCount = 0
	mu.Unlock()

	logInfo("Checker", "Updating vanity URL...")
	logInfo("Checker", "Initial vanity URL update attempted.")
	if err := connectGateway(); err != nil {
		logError("Gateway", fmt.Sprintf("Failed to connect to Discord Gateway: %v", err))
		os.Exit(1)
	}
	if err := identifyGateway(config.Token); err != nil {
		logError("Gateway", fmt.Sprintf("Failed to identify to Discord Gateway: %v", err))
		os.Exit(1)
	}

	go handleMessages(config.Token, config.GuildID, config.NewVanityURL, config.Password)
	go reconnect(config.Token, config.GuildID, config.NewVanityURL, config.Password)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	clearConsole()
	<-stop
	logInfo("Connection", "Shutting down gracefully...")
	if socket != nil {

		err := socket.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		if err != nil {
			logError("Connection", fmt.Sprintf("Error sending close message: %v", err))
		}
		socket.Close()
	}
	logInfo("Connection", "Shutdown complete.")
}
