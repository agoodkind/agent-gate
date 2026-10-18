text(await tools.exec_command({cmd: "git switch --detach " + ref, workdir: "PRIMARY"}));
text(await tools.exec_command({cmd: "git switch --detach origin/main", workdir: "WORKTREE"}));
