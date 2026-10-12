# shellcheck shell=bash
# SPDX-License-Identifier: GPL-3.0-or-later
# bash completion for tarcin

_tarcin() {
	local cur prev words cword
	_init_completion || return

	case $prev in
	--config | -config | --trace | -trace)
		_filedir
		return
		;;
	--profile | -profile | --demo-rate | -demo-rate | --pprof | -pprof)
		return
		;;
	esac

	local cmd='' i
	for ((i = 1; i < cword; i++)); do
		case ${words[i]} in
		logout | backup | config)
			cmd=${words[i]}
			break
			;;
		esac
	done

	local words_list
	case $cmd in
	logout)
		words_list='--profile'
		;;
	backup)
		if [[ $cur == -* ]]; then
			words_list='--profile --with-keys'
		else
			_filedir
			return
		fi
		;;
	config)
		if [[ $cur == -* ]]; then
			words_list='--config'
		else
			words_list='check path print-defaults'
		fi
		;;
	*)
		if [[ $cur == -* ]]; then
			words_list='--config --demo --demo-link --demo-rate --dump-keys --help --pprof --profile --trace --version'
		else
			words_list='logout backup config'
		fi
		;;
	esac
	mapfile -t COMPREPLY < <(compgen -W "$words_list" -- "$cur")
}

complete -F _tarcin tarcin
