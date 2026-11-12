package domain

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// SubtitleCharset 表示外挂文本字幕的字节编码。
type SubtitleCharset string

const (
	SubtitleCharsetUTF8    SubtitleCharset = "UTF-8"
	SubtitleCharsetUTF16LE SubtitleCharset = "UTF-16LE"
	SubtitleCharsetUTF16BE SubtitleCharset = "UTF-16BE"
	SubtitleCharsetGB18030 SubtitleCharset = "GB18030"
	SubtitleCharsetBig5    SubtitleCharset = "BIG5"
)

// 编码判定使用常见汉字命中率与替换字符比例，阈值按中文文本标定：
// 正确解码的常见字比例远高于错误解码，错误解码会大量产生 U+FFFD 或生僻字。
const (
	subtitleCharsetMinCommonRatio = 0.20
	subtitleCharsetMaxReplaceRate = 0.03
	subtitleCharsetDecisiveRatio  = 2
)

// DetectSubtitleCharset 判断外挂文本字幕的编码。
// label 用于在两个候选都合理时按“简体/繁体/BIG5/GBK”等标记取舍；无法可靠区分时返回
// subtitle_charset_unsupported，避免把乱码字幕当成可用候选。
func DetectSubtitleCharset(sample []byte, label string) (SubtitleCharset, error) {
	if len(sample) == 0 {
		return "", subtitleCharsetError("the subtitle file is empty")
	}
	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		return SubtitleCharsetUTF8, nil
	}
	if bytes.HasPrefix(sample, []byte{0xFF, 0xFE}) {
		return SubtitleCharsetUTF16LE, nil
	}
	if bytes.HasPrefix(sample, []byte{0xFE, 0xFF}) {
		return SubtitleCharsetUTF16BE, nil
	}
	if utf8.Valid(sample) {
		return SubtitleCharsetUTF8, nil
	}
	gb := scoreSubtitleCharset(sample, simplifiedchinese.GB18030.NewDecoder())
	big5 := scoreSubtitleCharset(sample, traditionalchinese.Big5.NewDecoder())
	gbUsable, big5Usable := gb.usable(), big5.usable()
	if !gbUsable && !big5Usable {
		return "", subtitleCharsetError("the subtitle text encoding is not UTF-8, GB18030, or BIG5")
	}
	if gbUsable != big5Usable {
		if gbUsable {
			return SubtitleCharsetGB18030, nil
		}
		return SubtitleCharsetBig5, nil
	}
	if gb.common >= big5.common*subtitleCharsetDecisiveRatio {
		return SubtitleCharsetGB18030, nil
	}
	if big5.common >= gb.common*subtitleCharsetDecisiveRatio {
		return SubtitleCharsetBig5, nil
	}
	simplified, traditional, _ := subtitleLabelSignals(label)
	if traditional != simplified {
		if traditional {
			return SubtitleCharsetBig5, nil
		}
		return SubtitleCharsetGB18030, nil
	}
	return "", subtitleCharsetError("the subtitle text encoding is ambiguous")
}

// DecodeSubtitleText 把检测出的编码转换为 UTF-8。
func DecodeSubtitleText(content []byte, charset SubtitleCharset) ([]byte, error) {
	var decoded []byte
	var err error
	switch charset {
	case SubtitleCharsetUTF8:
		decoded = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	case SubtitleCharsetUTF16LE, SubtitleCharsetUTF16BE:
		// UseBOM 同时处理 BOM 标记的实际字节序。
		decoded, _, err = transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), content)
	case SubtitleCharsetGB18030:
		decoded, _, err = transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), content)
	case SubtitleCharsetBig5:
		decoded, _, err = transform.Bytes(traditionalchinese.Big5.NewDecoder(), content)
	default:
		return nil, subtitleCharsetError("the subtitle text encoding is not supported")
	}
	if err != nil {
		return nil, subtitleCharsetError("the subtitle text could not be decoded")
	}
	if !utf8.Valid(decoded) || bytes.ContainsRune(decoded, utf8.RuneError) {
		return nil, subtitleCharsetError("the subtitle text contains invalid characters for its encoding")
	}
	return decoded, nil
}

type subtitleCharsetScore struct {
	common       int
	han          int
	replacements int
	total        int
}

func scoreSubtitleCharset(content []byte, decoder transform.Transformer) subtitleCharsetScore {
	decoded, _, err := transform.Bytes(decoder, content)
	if err != nil {
		return subtitleCharsetScore{}
	}
	score := subtitleCharsetScore{}
	for _, character := range string(decoded) {
		score.total++
		if character == utf8.RuneError {
			score.replacements++
			continue
		}
		if isHanCharacter(character) {
			score.han++
		}
		if isCommonSubtitleCharacter(character) {
			score.common++
		}
	}
	return score
}

func (score subtitleCharsetScore) usable() bool {
	if score.total == 0 || score.han == 0 {
		return false
	}
	if float64(score.common)/float64(score.han) < subtitleCharsetMinCommonRatio {
		return false
	}
	return float64(score.replacements)/float64(score.total) <= subtitleCharsetMaxReplaceRate
}

func isHanCharacter(character rune) bool {
	return character >= 0x4E00 && character <= 0x9FFF
}

// commonSubtitleCharacterText 覆盖现代中文与繁体字幕的高频字，用于区分正确与错误解码。
const commonSubtitleCharacterText = "的一是不了在人有我他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看很给等最间新什打便位因重被走电四第门相次东政海口使教西再平真听世气信北少关并内加化由却代军产入先山五太水万市眼体别处总才场师书比住员九笑性通目华报立马命张活难神数件安表原车白应路期叫死常提感金何更反合放做系计或司利受光王果亲界及今京务制解各任至清物台象记边共风战干接它许八特觉望直服毛林题建南度统色字请交爱让认算论百吃义科怎元社术结六功指思非流每青管夫连远资队跟带花快条院变联言权往展该领传近留红治决周保达办运武半候七必城父强步完革深区即求品士转量空甚众技轻程告江语英基派满式李息写呢识极令黄德收脸钱党倒未持取设始版双历越史商千片容研像找友孩站广改议形委早房音火际则首单据导影失拿网香似斯专石若兵弟谁校读志飞观争究包组造落视济喜离虽坐集编宝谈府拉黑且随格尽剑讲布杀微怕母调局根曾准赢坏项呀久妈尝误篇這來國個說們為時會著過裡後麼沒於還發當開經頭長兩現將與點兒給幾業間東關並內軍產員華報馬張覺難數動聽氣歡歲萬樂樣實處總場書車應讓認導飛雙歷議區轉聲從對學習漢語龍鳳愛戰權門問題請誰認識語讀寫標準確麗嚴單絲專業熱鬧圖書畫線練習題必須知識語言文字學生老師朋友學校家庭工作生活時間地方城市中國世界歷史文化藝術音樂電影電視新聞社會經濟政治科學教育環境自然風景旅遊美食健康運動電腦手機網絡技術發展改變希望夢想計劃準備開始結束繼續完成成功努力認真仔細注意安全危險緊急幫助支持關心照顧保護環境社會責任未來過去現在明天昨天今天早上下午晚上午飯早飯晚飯睡覺起床工作休息看書寫字說話聽音樂看電影玩遊戲上網購物做飯吃飯喝水喝茶咖啡牛奶麵包雞蛋水果蘋果香蕉橘子西瓜蔬菜肉魚蝦米飯麵條包子餃子湯菜餐廳服務員老闆顧客價格便宜貴質量好壞大小多少高低長短快慢早晚冷熱新舊乾淨漂亮難看舒服累病醫院醫生護士藥打針吃藥檢查身體健康薇爾莉特伊芙加登萊沙夫利希郵局基爾伯特少校往事逐漸清晰"

var commonSubtitleCharacters = func() map[rune]struct{} {
	set := make(map[rune]struct{}, len(commonSubtitleCharacterText))
	for _, character := range commonSubtitleCharacterText {
		set[character] = struct{}{}
	}
	return set
}()

func isCommonSubtitleCharacter(character rune) bool {
	_, ok := commonSubtitleCharacters[character]
	return ok
}

func subtitleCharsetError(reason string) error {
	return &SubtitleError{
		Code:    "subtitle_charset_unsupported",
		Message: fmt.Sprintf("the external subtitle encoding could not be used: %s; convert the subtitle to UTF-8 and retry", reason),
	}
}
