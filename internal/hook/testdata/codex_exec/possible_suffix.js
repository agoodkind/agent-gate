text(await tools.exec_command({cmd: prefix + " origin/main", workdir: "PRIMARY"}));
text(await tools.exec_command({cmd: "git switch --detach origin/main", workdir: "WORKTREE"}));
