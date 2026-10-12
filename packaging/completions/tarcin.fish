# SPDX-License-Identifier: GPL-3.0-or-later
# fish completion for tarcin

complete -c tarcin -f

# Commands
complete -c tarcin -n __fish_use_subcommand -a logout -d 'Unlink this device and delete its keys'
complete -c tarcin -n __fish_use_subcommand -a backup -d 'Write your messages and settings to a file'
complete -c tarcin -n __fish_use_subcommand -a config -d 'Validate, locate or print the configuration'
complete -c tarcin -n '__fish_seen_subcommand_from config; and not __fish_seen_subcommand_from check path print-defaults' -a 'check path print-defaults'
complete -c tarcin -n '__fish_seen_subcommand_from config' -l config -r -F -d 'The config file'
complete -c tarcin -n '__fish_seen_subcommand_from logout' -l profile -x -d 'The profile to log out of'
complete -c tarcin -n '__fish_seen_subcommand_from backup' -F
complete -c tarcin -n '__fish_seen_subcommand_from backup' -l profile -x -d 'The profile to back up'
complete -c tarcin -n '__fish_seen_subcommand_from backup' -l with-keys -d 'Add the device keys'

# Flags
complete -c tarcin -n __fish_use_subcommand -l profile -x -d 'The profile: one account with its own data'
complete -c tarcin -n __fish_use_subcommand -l config -r -F -d 'The config file'
complete -c tarcin -n __fish_use_subcommand -l demo -d 'Run with simulated chats'
complete -c tarcin -n __fish_use_subcommand -l demo-rate -x -d 'Live messages per second in the demo'
complete -c tarcin -n __fish_use_subcommand -l demo-link -d 'Start the demo as a device that is not linked yet'
complete -c tarcin -n __fish_use_subcommand -l dump-keys -d 'Show the names of the keys you press'
complete -c tarcin -n __fish_use_subcommand -l pprof -x -d 'For developers: serve Go profiles on a loopback address'
complete -c tarcin -n __fish_use_subcommand -l trace -r -F -d 'For developers: record an execution trace into a new file'
complete -c tarcin -n __fish_use_subcommand -l version -d 'Print version information and exit'
