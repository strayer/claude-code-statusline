# Draw terminal output as an SVG terminal window: a rounded, shadowed
# rectangle on a transparent canvas with one <text> per line. Understands what
# the statusline emits: SGR colors, bold and faint, and OSC 8 hyperlinks
# (dropped). Anything else is an error rather than a silently wrong picture.
#
# Usage: jq -nRr --arg font <family> -f ansi2svg.jq < output.ansi > output.svg

# Tokyo Night Storm. Default text is the gray Claude Code gives the statusline,
# not the terminal's foreground, which is ansi[15].
{
  bg: "#24283b",
  fg: "#999999",
  ansi: [
    "#1d202f", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
    "#414868", "#ff899d", "#9fe044", "#faba4a", "#8db0ff", "#c7a9ff", "#a4daff", "#c0caf5"
  ]
} as $theme

# Sizes in CSS pixels. A cell is half the font size wide, which is Iosevka's
# advance.
| {
  font: 16, line: 20, baseline: 15,
  pad: {x: 16, y: 12},
  margin: {x: 32, top: 24, bottom: 44},
  radius: 10
} as $g

| def xml: gsub("&"; "&amp;") | gsub("<"; "&lt;") | gsub(">"; "&gt;");

def sgr($params):
  reduce ($params | split(";") | if length == 0 then [""] else . end
          | .[] | if . == "" then 0 else tonumber end) as $c (.;
    if   $c == 0  then .bold = false | .faint = false | .fg = null
    elif $c == 1  then .bold = true
    elif $c == 2  then .faint = true
    elif $c == 22 then .bold = false | .faint = false
    elif $c == 39 then .fg = null
    elif $c >= 30 and $c <= 37 then .fg = $theme.ansi[$c - 30]
    elif $c >= 90 and $c <= 97 then .fg = $theme.ansi[$c - 82]
    else error("unsupported SGR parameter \($c)") end);

# Append text in the current style. Faint text is half transparent, which on
# the flat background is what terminals blend it to.
def put($text):
  if $text == "" then . else
    .cols += ($text | length)
    | .row += "<tspan fill=\"\(.fg // $theme.fg)\""
        + (if .bold then " font-weight=\"bold\"" else "" end)
        + (if .faint then " fill-opacity=\"0.5\"" else "" end)
        + ">\($text | xml)</tspan>"
  end;

# What follows one ESC: an SGR sequence or an OSC 8 link, then plain text.
def sequence($s):
  (($s | capture("^\\[(?<params>[0-9;]*)m(?<text>.*)$")) // null) as $style
  | (($s | capture("^\\]8;;[^\u0007]*\u0007(?<text>.*)$")) // null) as $link
  | if $style then sgr($style.params) | put($style.text)
    elif $link then put($link.text)
    else error("unsupported escape sequence: ESC \($s)") end;

# The style carries over from line to line, as it does in a terminal.
reduce inputs as $line ({bold: false, faint: false, fg: null, rows: [], width: 0};
  ($line | split("\u001b")) as $parts
  | .cols = 0 | .row = ""
  | put($parts[0] // "")
  | reduce $parts[1:][] as $s (.; sequence($s))
  | .rows += [.row]
  | .width = ([.width, .cols] | max))

| (.width * $g.font / 2 + 2 * $g.pad.x) as $w
| ((.rows | length) * $g.line + 2 * $g.pad.y) as $h
| ($w + 2 * $g.margin.x) as $W
| ($h + $g.margin.top + $g.margin.bottom) as $H
| "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"\($W)\" height=\"\($H)\" viewBox=\"0 0 \($W) \($H)\">",
  "<filter id=\"shadow\" filterUnits=\"userSpaceOnUse\" x=\"0\" y=\"0\" width=\"\($W)\" height=\"\($H)\">",
  "<feDropShadow dx=\"0\" dy=\"10\" stdDeviation=\"11\" flood-color=\"#000\" flood-opacity=\"0.5\"/>",
  "</filter>",
  "<rect x=\"\($g.margin.x)\" y=\"\($g.margin.top)\" width=\"\($w)\" height=\"\($h)\" rx=\"\($g.radius)\" fill=\"\($theme.bg)\" filter=\"url(#shadow)\"/>",
  "<g font-family=\"\($font)\" font-size=\"\($g.font)\" xml:space=\"preserve\">",
  (.rows | to_entries[] | select(.value != "")
   | "<text x=\"\($g.margin.x + $g.pad.x)\" y=\"\($g.margin.top + $g.pad.y + .key * $g.line + $g.baseline)\">\(.value)</text>"),
  "</g>",
  "</svg>"
