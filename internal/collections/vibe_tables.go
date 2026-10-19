package collections

import "strings"

// vibeGenre maps a query word to catalogue (TMDB) genre names. Movie
// and TV genre names differ ("Science Fiction" vs "Sci-Fi & Fantasy"),
// so both spellings are listed; catalogue Browse matches any of them.
type vibeGenre struct {
	genres []string
	plural string // title noun, e.g. "Thrillers" (may be "")
	adj    string // title adjective, e.g. "Horror"
	mood   string // extra title part for mood words ("Feel-Good")
	lang   string // implied original language (anime -> ja)
}

var vibeGenres = map[string]vibeGenre{
	"thriller":      {[]string{"Thriller"}, "Thrillers", "Thriller", "", ""},
	"thrillers":     {[]string{"Thriller"}, "Thrillers", "Thriller", "", ""},
	"horror":        {[]string{"Horror"}, "Horror", "Horror", "", ""},
	"scary":         {[]string{"Horror"}, "Horror", "Horror", "Scary", ""},
	"creepy":        {[]string{"Horror"}, "Horror", "Horror", "", ""},
	"comedy":        {[]string{"Comedy"}, "Comedies", "Comedy", "", ""},
	"comedies":      {[]string{"Comedy"}, "Comedies", "Comedy", "", ""},
	"funny":         {[]string{"Comedy"}, "Comedies", "Comedy", "Funny", ""},
	"hilarious":     {[]string{"Comedy"}, "Comedies", "Comedy", "", ""},
	"rom-com":       {[]string{"Romance", "Comedy"}, "Romantic Comedies", "Romantic Comedy", "", ""},
	"rom-coms":      {[]string{"Romance", "Comedy"}, "Romantic Comedies", "Romantic Comedy", "", ""},
	"romcom":        {[]string{"Romance", "Comedy"}, "Romantic Comedies", "Romantic Comedy", "", ""},
	"romance":       {[]string{"Romance"}, "Romance", "Romance", "", ""},
	"romantic":      {[]string{"Romance"}, "Romance", "Romantic", "", ""},
	"drama":         {[]string{"Drama"}, "Dramas", "Drama", "", ""},
	"dramas":        {[]string{"Drama"}, "Dramas", "Drama", "", ""},
	"action":        {[]string{"Action", "Action & Adventure"}, "Action", "Action", "", ""},
	"adventure":     {[]string{"Adventure", "Action & Adventure"}, "Adventure", "Adventure", "", ""},
	"crime":         {[]string{"Crime"}, "Crime", "Crime", "", ""},
	"mystery":       {[]string{"Mystery"}, "Mysteries", "Mystery", "", ""},
	"mysteries":     {[]string{"Mystery"}, "Mysteries", "Mystery", "", ""},
	"sci-fi":        {[]string{"Science Fiction", "Sci-Fi & Fantasy"}, "Sci-Fi", "Sci-Fi", "", ""},
	"scifi":         {[]string{"Science Fiction", "Sci-Fi & Fantasy"}, "Sci-Fi", "Sci-Fi", "", ""},
	"fantasy":       {[]string{"Fantasy", "Sci-Fi & Fantasy"}, "Fantasy", "Fantasy", "", ""},
	"animated":      {[]string{"Animation"}, "", "Animated", "", ""},
	"animation":     {[]string{"Animation"}, "", "Animated", "", ""},
	"anime":         {[]string{"Animation"}, "Anime", "Anime", "", "ja"},
	"cartoon":       {[]string{"Animation", "Family"}, "Cartoons", "Cartoon", "", ""},
	"cartoons":      {[]string{"Animation", "Family"}, "Cartoons", "Cartoon", "", ""},
	"family":        {[]string{"Family"}, "Family", "Family", "", ""},
	"kids":          {[]string{"Family", "Animation"}, "Kids", "Kids", "", ""},
	"kid":           {[]string{"Family", "Animation"}, "Kids", "Kids", "", ""},
	"documentary":   {[]string{"Documentary"}, "Documentaries", "Documentary", "", ""},
	"documentaries": {[]string{"Documentary"}, "Documentaries", "Documentary", "", ""},
	"docs":          {[]string{"Documentary"}, "Documentaries", "Documentary", "", ""},
	"war":           {[]string{"War", "War & Politics"}, "War", "War", "", ""},
	"western":       {[]string{"Western"}, "Westerns", "Western", "", ""},
	"westerns":      {[]string{"Western"}, "Westerns", "Western", "", ""},
	"history":       {[]string{"History"}, "History", "History", "", ""},
	"historical":    {[]string{"History", "Drama"}, "Historical", "Historical", "", ""},
	"music":         {[]string{"Music"}, "Music", "Music", "", ""},
	"musical":       {[]string{"Music"}, "Musicals", "Musical", "", ""},
	"musicals":      {[]string{"Music"}, "Musicals", "Musical", "", ""},
	"feel-good":     {[]string{"Comedy", "Family"}, "Comedies", "Comedy", "Feel-Good", ""},
	"feelgood":      {[]string{"Comedy", "Family"}, "Comedies", "Comedy", "Feel-Good", ""},
	"uplifting":     {[]string{"Comedy", "Family", "Drama"}, "Dramas", "Drama", "Uplifting", ""},
	"heartwarming":  {[]string{"Comedy", "Family", "Romance"}, "Comedies", "Comedy", "Heartwarming", ""},
	"cozy":          {[]string{"Comedy", "Family", "Romance"}, "Comedies", "Comedy", "Cozy", ""},
}

// vibePhrases are multi-word genre/mood forms.
var vibePhrases = map[string]vibeGenre{
	"science fiction":   {[]string{"Science Fiction", "Sci-Fi & Fantasy"}, "Sci-Fi", "Sci-Fi", "", ""},
	"feel good":         {[]string{"Comedy", "Family"}, "Comedies", "Comedy", "Feel-Good", ""},
	"romantic comedy":   {[]string{"Romance", "Comedy"}, "Romantic Comedies", "Romantic Comedy", "", ""},
	"romantic comedies": {[]string{"Romance", "Comedy"}, "Romantic Comedies", "Romantic Comedy", "", ""},
	"true crime":        {[]string{"Crime", "Documentary"}, "True Crime", "True Crime", "", ""},
	"action adventure":  {[]string{"Action", "Adventure", "Action & Adventure"}, "Action & Adventure", "Action & Adventure", "", ""},
}

type wordDecade struct {
	label string
	start int
}

// wordDecades maps spelled-out decades to (title label, first year).
var wordDecades = map[string]wordDecade{
	"fifties":   {"50s", 1950},
	"sixties":   {"60s", 1960},
	"seventies": {"70s", 1970},
	"eighties":  {"80s", 1980},
	"nineties":  {"90s", 1990},
	"noughties": {"2000s", 2000},
}

type vibeLang struct {
	lang      string   // ISO 639-1 original language
	countries []string // catalogue stores full names for movies, ISO codes for shows
	display   string
}

func langEntry(lang, name, iso, display string) vibeLang {
	return vibeLang{lang: lang, countries: []string{name, iso}, display: display}
}

// vibeLanguages maps language/country adjectives. Both the language
// and the country are applied, so "korean horror" means Korean cinema,
// not just any film dubbed or set there.
var vibeLanguages = map[string]vibeLang{
	"korean":      langEntry("ko", "South Korea", "KR", "Korean"),
	"japanese":    langEntry("ja", "Japan", "JP", "Japanese"),
	"french":      langEntry("fr", "France", "FR", "French"),
	"spanish":     langEntry("es", "Spain", "ES", "Spanish"),
	"german":      langEntry("de", "Germany", "DE", "German"),
	"italian":     langEntry("it", "Italy", "IT", "Italian"),
	"hindi":       langEntry("hi", "India", "IN", "Hindi"),
	"indian":      langEntry("hi", "India", "IN", "Indian"),
	"bollywood":   langEntry("hi", "India", "IN", "Bollywood"),
	"british":     langEntry("en", "United Kingdom", "GB", "British"),
	"american":    langEntry("en", "United States of America", "US", "American"),
	"australian":  langEntry("en", "Australia", "AU", "Australian"),
	"canadian":    langEntry("en", "Canada", "CA", "Canadian"),
	"irish":       langEntry("en", "Ireland", "IE", "Irish"),
	"chinese":     langEntry("zh", "China", "CN", "Chinese"),
	"mandarin":    langEntry("zh", "China", "CN", "Mandarin"),
	"taiwanese":   langEntry("zh", "Taiwan", "TW", "Taiwanese"),
	"thai":        langEntry("th", "Thailand", "TH", "Thai"),
	"swedish":     langEntry("sv", "Sweden", "SE", "Swedish"),
	"danish":      langEntry("da", "Denmark", "DK", "Danish"),
	"norwegian":   langEntry("no", "Norway", "NO", "Norwegian"),
	"finnish":     langEntry("fi", "Finland", "FI", "Finnish"),
	"dutch":       langEntry("nl", "Netherlands", "NL", "Dutch"),
	"portuguese":  langEntry("pt", "Portugal", "PT", "Portuguese"),
	"brazilian":   langEntry("pt", "Brazil", "BR", "Brazilian"),
	"russian":     langEntry("ru", "Russia", "RU", "Russian"),
	"polish":      langEntry("pl", "Poland", "PL", "Polish"),
	"turkish":     langEntry("tr", "Turkey", "TR", "Turkish"),
	"arabic":      langEntry("ar", "Egypt", "EG", "Arabic"),
	"persian":     langEntry("fa", "Iran", "IR", "Persian"),
	"iranian":     langEntry("fa", "Iran", "IR", "Iranian"),
	"indonesian":  langEntry("id", "Indonesia", "ID", "Indonesian"),
	"vietnamese":  langEntry("vi", "Vietnam", "VN", "Vietnamese"),
	"mexican":     langEntry("es", "Mexico", "MX", "Mexican"),
	"argentinian": langEntry("es", "Argentina", "AR", "Argentinian"),
	"greek":       langEntry("el", "Greece", "GR", "Greek"),
	"hungarian":   langEntry("hu", "Hungary", "HU", "Hungarian"),
	"czech":       langEntry("cs", "Czech Republic", "CZ", "Czech"),
	"romanian":    langEntry("ro", "Romania", "RO", "Romanian"),
	"ukrainian":   langEntry("uk", "Ukraine", "UA", "Ukrainian"),
	"hebrew":      langEntry("he", "Israel", "IL", "Hebrew"),
	"israeli":     langEntry("he", "Israel", "IL", "Israeli"),
}

// knownStudios maps a normalized studio shorthand to the catalogue
// studio name (as TMDB spells it) and a display form.
var knownStudios = map[string][2]string{
	"a24":           {"A24", "A24"},
	"pixar":         {"Pixar", "Pixar"},
	"disney":        {"Walt Disney Pictures", "Disney"},
	"dreamworks":    {"DreamWorks Animation", "DreamWorks"},
	"ghibli":        {"Studio Ghibli", "Studio Ghibli"},
	"studio ghibli": {"Studio Ghibli", "Studio Ghibli"},
	"marvel":        {"Marvel Studios", "Marvel"},
	"dc":            {"DC Films", "DC"},
	"blumhouse":     {"Blumhouse Productions", "Blumhouse"},
	"neon":          {"NEON", "Neon"},
	"searchlight":   {"Searchlight Pictures", "Searchlight"},
	"miramax":       {"Miramax", "Miramax"},
	"new line":      {"New Line Cinema", "New Line"},
	"lionsgate":     {"Lionsgate", "Lionsgate"},
	"paramount":     {"Paramount Pictures", "Paramount"},
	"warner":        {"Warner Bros. Pictures", "Warner Bros."},
	"warner bros":   {"Warner Bros. Pictures", "Warner Bros."},
	"universal":     {"Universal Pictures", "Universal"},
	"columbia":      {"Columbia Pictures", "Columbia"},
	"laika":         {"Laika", "Laika"},
	"aardman":       {"Aardman", "Aardman"},
	"illumination":  {"Illumination", "Illumination"},
}

// knownStudio resolves a lower-cased name to a known studio.
func knownStudio(name string) (studio, display string, ok bool) {
	v, ok := knownStudios[strings.TrimSpace(strings.ToLower(name))]
	if !ok {
		return "", "", false
	}
	return v[0], v[1], true
}
