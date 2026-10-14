text(await tools.exec_command({cmd: "git status", workdir: "PRIMARY"}));
text(await tools.exec_command({cmd: "git switch --detach origin/main", workdir: "WORKTREE"}));
