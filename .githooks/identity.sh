#!/bin/sh
# Sourced by pre-commit and pre-merge-commit: the canonical contributor
# identity lives in .env (GITHUB_NAME / GITHUB_EMAIL). Every commit and
# merge must carry exactly that identity; anything else is declined.
#
# The .env parsing mirrors cmd/server/envfile.go: surrounding whitespace,
# whole-line '#' comments, an optional 'export ' prefix, KEY=VALUE, one
# wrapping quote pair and inline ' #' comments.

# strip_value trims one raw .env value: spaces, wrapping quotes, inline
# comment, trailing spaces again.
strip_value() {
	v=${1#"${1%%[![:space:]]*}"} # ltrim
	v=${v%"${v##*[![:space:]]}"} # rtrim
	case "$v" in
		'"'*'"') v=${v#\"}; v=${v%\"} ;;
		"'"*"'") v=${v#\'}; v=${v%\'} ;;
	esac
	v=${v%%' #'*}
	v=${v%"${v##*[![:space:]]}"} # rtrim
	v=${v#"${v%%[![:space:]]*}"} # ltrim
	printf '%s' "$v"
}

# identity_want sets WANT_NAME / WANT_EMAIL from the repo .env.
identity_want() {
	WANT_NAME=''
	WANT_EMAIL=''
	[ -f .env ] || return 0
	while IFS= read -r line || [ -n "$line" ]; do
		line=${line#"${line%%[![:space:]]*}"}
		case "$line" in
			'' | '#'*) continue ;;
		esac
		line=${line#export[[:space:]]}
		case "$line" in
			*=*) ;;
			*) continue ;;
		esac
		key=${line%%=*}
		val=${line#*=}
		key=${key%"${key##*[![:space:]]}"} # rtrim
		key=${key#"${key%%[![:space:]]*}"} # ltrim
		val=$(strip_value "$val")
		case "$key" in
			GITHUB_NAME) WANT_NAME=$val ;;
			GITHUB_EMAIL) WANT_EMAIL=$val ;;
		esac
	done < .env
}

# identity_check exits non-zero when the commit about to be created would
# not carry the identity from .env. Env vars beat git config, mirroring
# git's own precedence; both the author and the committer are checked.
identity_check() {
	root=$(git rev-parse --show-toplevel) || exit 1
	cd "$root" || exit 1
	identity_want
	if [ -z "$WANT_NAME" ] || [ -z "$WANT_EMAIL" ]; then
		echo >&2 "BLOCKED: no contributor identity configured."
		echo >&2 "        Add to .env (see .env.example):"
		echo >&2 "        GITHUB_NAME=extube"
		echo >&2 "        GITHUB_EMAIL=extube@proton.me"
		exit 1
	fi
	a_name=${GIT_AUTHOR_NAME:-$(git config user.name 2>/dev/null)}
	a_mail=${GIT_AUTHOR_EMAIL:-$(git config user.email 2>/dev/null)}
	c_name=${GIT_COMMITTER_NAME:-$(git config user.name 2>/dev/null)}
	c_mail=${GIT_COMMITTER_EMAIL:-$(git config user.email 2>/dev/null)}
	if [ "$a_name" != "$WANT_NAME" ] || [ "$a_mail" != "$WANT_EMAIL" ] ||
		[ "$c_name" != "$WANT_NAME" ] || [ "$c_mail" != "$WANT_EMAIL" ]; then
		echo >&2 "BLOCKED: commits are only accepted as $WANT_NAME <$WANT_EMAIL> (GITHUB_NAME / GITHUB_EMAIL in .env)."
		echo >&2 "        This commit would carry: author ${a_name:-unset} <${a_mail:-unset}>, committer ${c_name:-unset} <${c_mail:-unset}>"
		echo >&2 "        Fix with:  git config user.name \"$WANT_NAME\" && git config user.email \"$WANT_EMAIL\""
		exit 1
	fi
}

identity_check
