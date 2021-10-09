#!/usr/bin/env python3
from subprocess import run, PIPE
from pathlib import Path
import re

content = run(['go', 'run', './cmd/wd/main.go', '--help'], check=False, stdout=PIPE).stdout.decode()
override = {
    'DISABLE_METRICS': 'true',
    'RUN_AS_SCRIPT_OWNER': 'true',
}

with (Path(__file__).parent / "deploy" / "systemd" / "webhooks.env").open('wt') as f:
    for line in content.splitlines():
        if '$' not in line:
            continue
        idx = line.index('--')
        sep = line.find(' ', idx)
        full_arg = line[idx:sep]
        is_bool = '=' not in full_arg
        options = (re.findall(r'\[(.+)\]', full_arg) or [''])[0]
        line = line[sep:].strip()
        default_value = (re.findall(r'\(default: (.*?)\)', line) or [''])[-1]
        env_arg = re.findall(r'\[\$(.+?)\]', line)[-1]
        if default_value:
            line = line[:line.rindex('(default: ')].strip()
        else:
            line = line[:line.rindex('[')]
        if not default_value and is_bool:
            default_value = 'false'
        value = override.get(env_arg, default_value)
        f.write(f'# {line}\n')
        if options:
            f.write(f'# Valid options: {options}\n')
        f.write(f'{env_arg}={value}\n')
