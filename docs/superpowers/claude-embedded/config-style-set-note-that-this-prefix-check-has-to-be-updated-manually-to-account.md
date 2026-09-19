<!--
  Extracted from Claude Code v2.1.278
  Source offset: 58235626
  Content hash: 9c5d7285863e7f4c
  Category: config
  Auto-generated — do not edit manually
-->


  # style set. Note that this prefix check has to be updated manually to account
  # for all of the potential negation options listed below!
  if
    # We also want to list all of these options during testing
    [[ $_RG_COMPLETE_LIST_ARGS == (1|t*|y*) ]] ||
    # (--[imnp]* => --ignore*, --messages, --no-*, --pcre2-unicode)
    [[ $PREFIX$SUFFIX == --[imnp]* ]] ||
    zstyle -t ":completion::" complete-all
  then
    no=
  fi

  # We make heavy use of argument groups here to prevent the option specs from
  # growing unwieldy. These aren't supported in zsh <5.4, though, so we'll strip
  # them out below if necessary. This makes the exclusions inaccurate on those
  # older versions, but oh well — it's not that big a deal
  args=(
    + '(exclusive)' # Misc. fully exclusive options
    '(: * -)'{-h,--help}'[display help information]'
    '(: * -)'{-V,--version}'[display version information]'
    '(: * -)'--pcre2-version'[print the version of PCRE2 used by ripgrep, if available]'

    + '(buffered)' # buffering options
    '--line-buffered[force line buffering]'
    $no"--no-line-buffered[don't force line buffering]"
    '--block-buffered[force block buffering]'
    $no"--no-block-buffered[don't force block buffering]"

    + '(case)' # Case-sensitivity options
    {-i,--ignore-case}'[search case-insensitively]'
    {-s,--case-sensitive}'[search case-sensitively]'
    {-S,--smart-case}'[search case-insensitively if pattern is all lowercase]'

    + '(context-a)' # Context (after) options
    '(context-c)'{-A+,--after-context=}'[specify lines to show after each match]:number of lines'

    + '(context-b)' # Context (before) options
    '(context-c)'{-B+,--before-context=}'[specify lines to show before each match]:number of lines'

    + '(context-c)' # Context (combined) options
    '(context-a context-b)'{-C+,--context=}'[specify lines to show before and after each match]:number of lines'

    + '(column)' # Column options
    '--column[show column numbers for matches]'
    $no"--no-column[don't show column numbers for matches]"

    + '(count)' # Counting options
    {-c,--count}'[only show count of matching lines for each file]'
    '--count-matches[only show count of individual matches for each file]'
    '--include-zero[include files with zero matches in summary]'
    $no"--no-include-zero[don't include files with zero matches in summary]"

    + '(encoding)' # Encoding options
    {-E+,--encoding=}'[specify text encoding of files to search]: :_rg_encodings'
    $no'--no-encoding[use default text encoding]'

    + '(engine)' # Engine choice options
    '--engine=[select which regex engine to use]:when:((
      default\:"use default engine"
      pcre2\:"identical to --pcre2"
      auto\:"identical to --auto-hybrid-regex"
    ))'

    + file # File-input options
    '(1)*'{-f+,--file=}'[specify file containing patterns to search for]: :_files'

    + '(file-match)' # Files with/without match options
    '(stats)'{-l,--files-with-matches}'[only show names of files with matches]'
    '(stats)--files-without-match[only show names of files without matches]'

    + '(file-name)' # File-name options
    {-H,--with-filename}'[show file name for matches]'
    {-I,--no-filename}"[don't show file name for matches]"

    + '(file-system)' # File system options
    "--one-file-system[don't descend into directories on other file systems]"
    $no'--no-one-file-system[descend into directories on other file systems]'

    + '(fixed)' # Fixed-string options
    {-F,--fixed-strings}'[treat pattern as literal string instead of regular expression]'
    $no"--no-fixed-strings[don't treat pattern as literal string]"

    + '(follow)' # Symlink-following options
    {-L,--follow}'[follow symlinks]'
    $no"--no-follow[don't follow symlinks]"

    + '(generate)' # Options for generating ancillary data
    '--generate=[generate man page or completion scripts]:when:((
      man\:"man page"
      complete-bash\:"shell completions for bash"
      complete-zsh\:"shell completions for zsh"
      complete-fish\:"shell completions for fish"
      complete-powershell\:"shell completions for PowerShell"
    ))'

    + glob # File-glob options
    '*'{-g+,--glob=}'[include/exclude files matching specified glob]:glob'
    '*--iglob=[include/exclude files matching specified case-insensitive glob]:glob'

    + '(glob-case-insensitive)' # File-glob case sensitivity options
    '--glob-case-insensitive[treat -g/--glob patterns case insensitively]'
    $no'--no-glob-case-insensitive[treat -g/--glob patterns case sensitively]'

    + '(heading)' # Heading options
    '(pretty-vimgrep)--heading[show matches grouped by file name]'
    "(pretty-vimgrep)--no-heading[don't show matches grouped by file name]"

    + '(hidden)' # Hidden-file options
    {-.,--hidden}'[search hidden files and directories]'
    $no"--no-hidden[don't search hidden files and directories]"

    + '(hybrid)' # hybrid regex options
    '--auto-hybrid-regex[DEPRECATED: dynamically use PCRE2 if necessary]'
    $no"--no-auto-hybrid-regex[DEPRECATED: don't dynamically use PCRE2 if necessary]"

    + '(ignore)' # Ignore-file options
    "(--no-ignore-global --no-ignore-parent --no-ignore-vcs --no-ignore-dot)--no-ignore[don't respect ignore files]"
    $no'(--ignore-global --ignore-parent --ignore-vcs --ignore-dot)--ignore[respect ignore files]'

    + '(ignore-file-case-insensitive)' # Ignore-file case sensitivity options
    '--ignore-file-case-insensitive[process ignore files case insensitively]'
    $no'--no-ignore-file-case-insensitive[process ignore files case sensitively]'

    + '(ignore-exclude)' # Local exclude (ignore)-file options
    "--no-ignore-exclude[don't respect local exclude (ignore) files]"
    $no'--ignore-exclude[respect local exclude (ignore) files]'

    + '(ignore-global)' # Global ignore-file options
    "--no-ignore-global[don't respect global ignore files]"
    $no'--ignore-global[respect global ignore files]'

    + '(ignore-parent)' # Parent ignore-file options
    "--no-ignore-parent[don't respect ignore files in parent directories]"
    $no'--ignore-parent[respect ignore files in parent directories]'

    + '(ignore-vcs)' # VCS ignore-file options
    "--no-ignore-vcs[don't respect version control ignore files]"
    $no'--ignore-vcs[respect version control ignore files]'

    + '(require-git)' # git specific settings
    "--no-require-git[don't require git repository to respect gitignore rules]"
    $no'--require-git[require git repository to respect gitignore rules]'

    + '(ignore-dot)' # .ignore options
    "--no-ignore-dot[don't respect .ignore files]"
    $no'--ignore-dot[respect .ignore files]'

    + '(ignore-files)' # custom global ignore file options
    "--no-ignore-files[don't respect --ignore-file flags]"
    $no'--ignore-files[respect --ignore-file files]'

    + '(json)' # JSON options
    '--json[output results in JSON Lines format]'
    $no"--no-json[don't output results in JSON Lines format]"

    + '(line-number)' # Line-number options
    {-n,--line-number}'[show line numbers for matches]'
    {-N,--no-line-number}"[don't show line numbers for matches]"

    + '(line-terminator)' # Line-terminator options
    '--crlf[use CRLF as line terminator]'
    $no"--no-crlf[don't use CRLF as line terminator]"
    '(text)--null-data[use NUL as line terminator]'

    + '(max-columns-preview)' # max column preview options
    '--max-columns-preview[show preview for long lines (with -M)]'
    $no"--no-max-columns-preview[don't show preview for long lines (with -M)]"

    + '(max-depth)' # Directory-depth options
    {-d,--max-depth}'[specify max number of directories to descend]:number of directories'
    '--maxdepth=[alias for --max-depth]:number of directories'
    '!--maxdepth=:number of directories'

    + '(messages)' # Error-message options
    '(--no-ignore-messages)--no-messages[suppress some error messages]'
    $no"--messages[don't suppress error messages affected by --no-messages]"

    + '(messages-ignore)' # Ignore-error message options
    "--no-ignore-messages[don't show ignore-file parse error messages]"
    $no'--ignore-messages[show ignore-file parse error messages]'

    + '(mmap)' # mmap options
    '--mmap[search using memory maps when possible]'
    "--no-mmap[don't search using memory maps]"

    + '(multiline)' # Multiline options
    {-U,--multiline}'[permit matching across multiple lines]'
    $no'(multiline-dotall)--no-multiline[restrict matches to at most one line each]'

    + '(multiline-dotall)' # Multiline DOTALL options
    '(--no-multiline)--multiline-dotall[allow "." to match newline (with -U)]'
    $no"(--no-multiline)--no-multiline-dotall[don't allow \".\" to match newline (with -U)]"

    + '(only)' # Only-match options
    {-o,--only-matching}'[show only matching part of each line]'

    + '(passthru)' # Pass-through options
    '(--vimgrep)--passthru[show both matching and non-matching lines]'
    '(--vimgrep)--passthrough[alias for --passthru]'

    + '(pcre2)' # PCRE2 options
    {-P,--pcre2}'[enable matching with PCRE2]'
    $no'(pcre2-unicode)--no-pcre2[disable matching with PCRE2]'

    + '(pcre2-unicode)' # PCRE2 Unicode options
    $no'(--no-pcre2 --no-pcre2-unicode)--pcre2-unicode[DEPRECATED: enable PCRE2 Unicode mode (with -P)]'
    '(--no-pcre2 --pcre2-unicode)--no-pcre2-unicode[DEPRECATED: disable PCRE2 Unicode mode (with -P)]'

    + '(pre)' # Preprocessing options
    '(-z --search-zip)--pre=[specify preprocessor utility]:preprocessor utility:_command_names -e'
    $no'--no-pre[disable preprocessor utility]'

    + pre-glob # Preprocessing glob options
    '*--pre-glob[include/exclude files for preprocessing with --pre]'

    + '(pretty-vimgrep)' # Pretty/vimgrep display options
    '(heading)'{-p,--pretty}'[alias for --color=always --heading -n]'
    '(heading passthru)--vimgrep[show results in vim-compatible format]'

    + regexp # Explicit pattern options
    '(1 file)*'{-e+,--regexp=}'[specify pattern]:pattern'

    + '(replace)' # Replacement options
    {-r+,--replace=}'[specify string used to replace matches]:replace string'

    + '(sort)' # File-sorting options
    '(threads)--sort=[sort results in ascending order (disables parallelism)]:sort method:((
      none\:"no sorting"
      path\:"sort by file path"
      modified\:"sort by last modified time"
      accessed\:"sort by last accessed time"
      created\:"sort by creation time"
    ))'
    '(threads)--sortr=[sort results in descending order (disables parallelism)]:sort method:((
      none\:"no sorting"
      path\:"sort by file path"
      modified\:"sort by last modified time"
      accessed\:"sort by last accessed time"
      created\:"sort by creation time"
    ))'
    '(threads)--sort-files[DEPRECATED: sort results by file path (disables parallelism)]'
    $no"--no-sort-files[DEPRECATED: do not sort results]"

    + '(stats)' # Statistics options
    '(--files file-match)--stats[show search statistics]'
    $no"--no-stats[don't show search statistics]"

    + '(text)' # Binary-search options
    {-a,--text}'[search binary files as if they were text]'
    "--binary[search binary files, don't print binary data]"
    $no"--no-binary[don't search binary files]"
    $no"(--null-data)--no-text[don't search binary files as if they were text]"

    + '(threads)' # Thread-count options
    '(sort)'{-j+,--threads=}'[specify approximate number of threads to use]:number of threads'

    + '(trim)' # Trim options
    '--trim[trim any ASCII whitespace prefix from each line]'
    $no"--no-trim[don't trim ASCII whitespace prefix from each line]"

    + type # Type options
    '*'{-t+,--type=}'[only search files matching specified type]: :_rg_types'
    '*--type-add=[add new glob for specified file type]: :->typespec'
    '*--type-clear=[clear globs previously defined for specified file type]: :_rg_types'
    # This should actually be exclusive with everything but other type options
    '(: *)--type-list[show all supported file types and their associated globs]'
    '*'{-T+,--type-not=}"[don't search files matching specified file type]: :_rg_types"

    + '(word-line)' # Whole-word/line match options
    {-w,--word-regexp}'[only show matches surrounded by word boundaries]'
    {-x,--line-regexp}'[only show matches surrounded by line boundaries]'

    + '(unicode)' # Unicode options
    $no'--unicode[enable Unicode mode]'
    '--no-unicode[disable Unicode mode]'

    + '(zip)' # Compression options
    '(--pre)'{-z,--search-zip}'[search in compressed files]'
    $no"--no-search-zip[don't search in compressed files]"

    + misc # Other options — no need to separate these at the moment
    '(-b --byte-offset)'{-b,--byte-offset}'[show 0-based byte offset for each matching line]'
    $no"--no-byte-offset[don't show byte offsets for each matching line]"
    '--color=[specify when to use colors in output]:when:((
      never\:"never use colors"
      auto\:"use colors or not based on stdout, TERM, etc."
      always\:"always use colors"
      ansi\:"always use ANSI colors (even on Windows)"
    ))'
    '*--colors=[specify color and style settings]: :->colorspec'
    '--context-separator=[specify string used to separate non-continuous context lines in output]:separator'
    $no"--no-context-separator[don't print context separators]"
    '--debug[show debug messages]'
    '--field-context-separator[set string to delimit fields in context lines]'
    '--field-match-separator[set string to delimit fields in matching lines]'
    '--hostname-bin=[executable for getting system hostname]:hostname executable:_command_names -e'
    '--hyperlink-format=[specify pattern for hyperlinks]:pattern'
    '--trace[show more verbose debug messages]'
    '--dfa-size-limit=[specify upper size limit of generated DFA]:DFA size (bytes)'
    "(1 stats)--files[show each file that would be searched (but don't search)]"
    '*--ignore-file=[specify additional ignore file]:ignore file:_files'
    '(-v --invert-match)'{-v,--invert-match}'[invert matching]'
    $no"--no-invert-match[do not invert matching]"
    '(-M --max-columns)'{-M+,--max-columns=}'[specify max length of lines to print]:number of bytes'
    '(-m --max-count)'{-m+,--max-count=}'[specify max number of matches per file]:number of matches'
    '--max-filesize=[specify size above which files should be ignored]:file size (bytes)'
    "--no-config[don't load configuration files]"
    '(-0 --null)'{-0,--null}'[print NUL byte after file names]'
    '--path-separator=[specify path separator to use when printing file names]:separator'
    '(-q --quiet)'{-q,--quiet}'[suppress normal output]'
    '--regex-size-limit=[specify upper size limit of compiled regex]:regex size (bytes)'
    '*'{-u,--unrestricted}'[reduce level of "smart" searching]'
    '--stop-on-nonmatch[stop on first non-matching line after a matching one]'

    + operand # Operands
    '(--files --type-list file regexp)1: :_guard "^-*" pattern'
    '(--type-list)*: :_files'
  )

  # This is used with test-complete to verify that there are no options
  # listed in the help output that aren't also defined here
  [[ $_RG_COMPLETE_LIST_ARGS == (1|t*|y*) ]] && {
    print -rl - $args
    return 0
  }

  # Strip out argument groups where unsupported (see above)
  [[ $ZSH_VERSION == (4|5.<0-3>)(.*)# ]] &&
  args=(  )

  _arguments -C -s -S : $args && ret=0

  case $state in
    colorspec)
      if [[ $PREFIX == [^:]# ]]; then
        suf=( -qS: )
        tmp=(
          'column:specify coloring for column numbers'
          'line:specify coloring for line numbers'
          'match:specify coloring for match text'
          'path:specify coloring for file names'
        )
        descr='color/style type'
      elif [[ $PREFIX == (column|line|match|path):[^:]# ]]; then
        suf=( -qS: )
        tmp=(
          'none:clear color/style for type'
          'bg:specify background color'
          'fg:specify foreground color'
          'style:specify text style'
        )
        descr='color/style attribute'
      elif [[ $PREFIX == [^:]##:(bg|fg):[^:]# ]]; then
        tmp=( black blue green red cyan magenta yellow white )
        descr='color name or r,g,b'
      elif [[ $PREFIX == [^:]##:style:[^:]# ]]; then
        tmp=( {,no}bold {,no}intense {,no}underline )
        descr='style name'
      else
        _message -e colorspec 'no more arguments'
      fi

      (( $#tmp )) && {
        compset -P '*:'
        _describe -t colorspec $descr tmp $suf && ret=0
      }
      ;;

    typespec)
      if compset -P '[^:]##:include:'; then
        _sequence -s , _rg_types && ret=0
      # @todo This bit in particular could be better, but it's a little
      # complex, and attempting to solve it seems to run us up against a crash
      # bug — zsh # 40362
      elif compset -P '[^:]##:'; then
        _message 'glob or include directive' && ret=1
      elif [[ ! -prefix *:* ]]; then
        _rg_types -qS : && ret=0
      fi
      ;;
  esac

  return ret
}

# Complete encodings
_rg_encodings() {
  local -a expl
  local -aU _encodings

  _encodings=(
!ENCODINGS!
  )

  _wanted encodings expl encoding compadd -a "$@" - _encodings
}

# Complete file types
_rg_types() {
  local -a expl
  local -aU _types

  _types=( //:[[:space:]]##/:} )

  if zstyle -t ":completion::types" extra-verbose; then
    _describe -t types 'file type' _types
  else
    _wanted types expl 'file type' compadd "$@" - 
  fi
}

_rg "$@"

################################################################################
# ZSH COMPLETION REFERENCE
#
# For the convenience of developers who aren't especially familiar with zsh
# completion functions, a brief reference guide follows. This is in no way
# comprehensive; it covers just enough of the basic structure, syntax, and
# conventions to help someone make simple changes like adding new options. For
# more complete documentation regarding zsh completion functions, please see the
# following:
#
# * http://zsh.sourceforge.net/Doc/Release/Completion-System.html
# * https://github.com/zsh-users/zsh/blob/master/Etc/completion-style-guide
#
# OVERVIEW
#
# Most zsh completion functions are defined in terms of