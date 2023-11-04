package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/aatomu/atomicgo/discordbot"
	"github.com/aatomu/atomicgo/utils"
	"github.com/aatomu/slashlib"
	"github.com/bwmarrin/discordgo"
)

// 保存するデータ
type Settings struct {
	save   sync.Mutex
	guilds map[string]Setting
}

type Setting struct {
	channelID string   // 反応するチャンネル
	logs      []string //連鎖用の元データ
	level     int      // マルコフ連鎖の階
	auto      int      // 勝手に反応する確率
}

var (
	//変数定義
	token  = flag.String("token", "", "bot token")
	learns = Settings{
		guilds: map[string]Setting{},
	}
	embedColor = 0x7CFC00
)

func main() {
	//flag入手
	flag.Parse()
	fmt.Println("botToken        :", *token)

	//bot起動準備
	discord, err := discordbot.Init(*token)
	if err != nil {
		fmt.Println("Error", err)
		return
	}

	//eventトリガー設定
	discord.AddHandler(onReady)
	discord.AddHandler(onMessageCreate)
	discord.AddHandler(onInteractionCreate)

	//起動
	discordbot.Start(discord)
	defer func() {
		for _, session := range learns.guilds {
			discord.ChannelMessageSendEmbed(session.channelID, &discordgo.MessageEmbed{
				Type:        "rich",
				Title:       "__Infomation__",
				Description: "Sorry. Bot will Shutdown. Will be try later.",
				Color:       embedColor,
			})
		}
		discord.Close()
	}()
	//bot停止対策
	<-utils.BreakSignal()
}

func onReady(discord *discordgo.Session, r *discordgo.Ready) {
	//起動メッセージ
	fmt.Println("Bot is OnReady now!")

	var levelMin float64 = 1
	var autoMin float64 = 0
	// コマンドの追加
	new(slashlib.Command).
		AddCommand("print", "学習データからしゃべります", discordgo.PermissionViewChannel).
		AddCommand("set", "学習設定を変更します", discordgo.PermissionViewChannel).
		AddOption(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionInteger,
			Name:        "level",
			Description: "学習精度を調整します",
			MinValue:    &levelMin,
			MaxValue:    3,
		}).
		AddOption(&discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionInteger,
			Name:        "auto",
			Description: "自動でしゃべるかを設定します",
			MinValue:    &autoMin,
			MaxValue:    100,
		}).
		CommandCreate(discord, "")
}

// メッセージが送られたときにCall
func onMessageCreate(discord *discordgo.Session, m *discordgo.MessageCreate) {
	joinedGuilds := len(discord.State.Guilds)
	discordbot.BotStateUpdate(discord, fmt.Sprintf("%d鯖で勉強中", joinedGuilds), 0)

	mData := discordbot.MessageParse(discord, m)
	log.Println(mData.FormatText)

	if m.Author.Bot {
		return
	}

	setting, ok := learns.guilds[m.GuildID]
	if !ok {
		return
	}

	text := Format(m.Content)
	if text != "" {
		setting.logs = append(setting.logs, text)
		learns.Set(m.GuildID, setting)
	}
	if len(setting.logs) > 150 {
		setting.logs = setting.logs[1:]
		learns.Set(m.GuildID, setting)
	}

	if setting.channelID == mData.ChannelID {
		rand := utils.Rand(100) + 1
		if rand <= setting.auto {
			model := MarkovModel(setting.logs, setting.level)
			discord.ChannelMessageSend(m.ChannelID, MarkovChain(model, setting.level))
		}
	}
}

// InteractionCreate
func onInteractionCreate(discord *discordgo.Session, iData *discordgo.InteractionCreate) {
	// 表示&処理しやすく
	i := slashlib.InteractionViewAndEdit(discord, iData)

	// slashじゃない場合return
	if i.Check != slashlib.SlashCommand {
		return
	}

	// response用データ
	res := slashlib.InteractionResponse{
		Discord:     discord,
		Interaction: iData.Interaction,
	}

	// 分岐
	switch i.Command.Name {
	case "print":
		res.Thinking(false)

		setting, ok := learns.guilds[i.GuildID]
		if !ok {
			setting, ok = learns.New(discord, i.GuildID, i.ChannelID)
			if !ok {
				Failed(res, "Failed Read Setting")
			}
		}

		model := MarkovModel(setting.logs, setting.level)
		res.Follow(&discordgo.WebhookParams{
			Content: MarkovChain(model, setting.level),
		})

		return

	case "set":
		res.Thinking(false)

		// 読み込み
		setting, ok := learns.guilds[i.GuildID]
		if !ok {
			setting, ok = learns.New(discord, i.GuildID, i.ChannelID)
			if !ok {
				Failed(res, "Failed Create New Setting")
			}
		}
		// チェック
		if newLevel, ok := i.CommandOptions["level"]; ok {
			setting.level = int(newLevel.IntValue())
		}

		if newAuto, ok := i.CommandOptions["auto"]; ok {
			setting.auto = int(newAuto.IntValue())
		}
		var autoLineText string
		if setting.auto > 0 {
			setting.channelID = i.ChannelID
			autoLineText = fmt.Sprintf("Auto : %d%% (<#%s>)", setting.auto, i.ChannelID)
		} else {
			setting.channelID = ""
			autoLineText = "Auto : false"
		}

		learns.Set(i.GuildID, setting)

		Success(res, fmt.Sprintf("Level : %d\n%s", setting.level, autoLineText))
	}
}

func MarkovModel(sentences []string, n int) (model map[string][]string) {
	// 初期化
	model = map[string][]string{}
	// 文ごとに
	for _, sentence := range sentences {
		// 分解
		words := strings.Split(sentence, "")
		// SOF,EOF追加
		words = append([]string{"#SOF"}, words...)
		words = append(words, "#EOF")
		// kv生成
		startkeys := []string{"#SOF"}
		// 最初の小さい部分
		for i := 1; len(startkeys) < n; i++ {
			key := strings.Join(startkeys, "")
			model[key] = append(model[key], words[i])
			startkeys = append(startkeys, words[i])
		}
		for i := 0; i+n < len(words); i++ {
			key := strings.Join(words[i:i+n], "")
			value := words[i+n]
			model[key] = append(model[key], value)
		}
	}
	return
}

func MarkovChain(model map[string][]string, n int) (text string) {
	var words = []string{"#SOF"}
	// 最初の小さい部分
	for i := 1; len(words) < n; i++ {
		words = append(words, RandChoice(model[strings.Join(words[0:i], "")]))
	}
	// 残り
	for i := 0; words[len(words)-1] != "#EOF" && i < 200; i++ {
		words = append(words, RandChoice(model[strings.Join(words[i:i+n], "")]))
	}
	// #SOFと#EOFを抜いて結合
	text = strings.Join(words[1:len(words)-1], "")
	return
}

func RandChoice(arr []string) string {
	rand.Seed(time.Now().UnixNano())
	i := rand.Intn(len(arr))
	return arr[i]
}

func (setting *Settings) New(discord *discordgo.Session, guildID, channelID string) (s Setting, ok bool) {
	s = Setting{
		channelID: "",
		logs:      []string{},
		level:     2,
		auto:      0,
	}

	messages, err := discord.ChannelMessages(channelID, 100, "", "", "")
	if err != nil {
		return Setting{}, false
	}
	for _, m := range messages {
		if m.Author.Bot || m.Content == "" {
			continue
		}
		text := Format(m.Content)
		if text == "" {
			continue
		}
		s.logs = append(s.logs, text)

	}
	setting.guilds[guildID] = s
	return s, true
}

func (settings *Settings) Set(guildID string, newSetting Setting) {
	settings.save.Lock()
	defer settings.save.Unlock()

	settings.guilds[guildID] = newSetting
}

func Format(text string) (s string) {
	// 。で改行
	s = utils.RegReplace(text, "。\n", "。+")
	// 特殊文字,連続空白 を削除
	s = utils.RegReplace(s, "", `[『』「」\[\]（|）\(\)\d\r\t]| {2,}`)
	// 絵文字とか削除
	s = utils.RegReplace(s, "", `<.+?>`)
	// URL削除
	s = utils.RegReplace(s, "", `https?://[\w!?/+\-_~:;.,=*&@#$%()'[\]]+`)
	// 連続改行を一つに
	s = utils.RegReplace(s, "\n", "\n+")
	return
}

func Failed(res slashlib.InteractionResponse, comment string) {
	res.Follow(&discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "Command Failed",
				Color:       embedColor,
				Description: comment,
			},
		},
	})
}

func Success(res slashlib.InteractionResponse, comment string) {
	res.Follow(&discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{
			{
				Title:       "Command Success",
				Color:       embedColor,
				Description: comment,
			},
		},
	})
}
