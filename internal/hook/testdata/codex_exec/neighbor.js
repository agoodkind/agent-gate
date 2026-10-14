text(await tools.exec_command({cmd: "git switch --detach origin/main"}));
text(await tools.exec_command({cmd: "git status", workdir: "PRIMARY"}));
