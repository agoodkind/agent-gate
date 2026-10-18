const worktree = "WORKTREE";
for (const path of ["source.go"]) {
  text(await tools.exec_command({cmd: "git show HEAD:" + path, workdir: "PRIMARY"}));
}
text(await tools.exec_command({cmd: "git switch --detach origin/main", workdir: worktree}));
