const workdir = "WORKTREE";
for (const path of ["source.sh"]) {
  text(await tools.exec_command({cmd: "git show HEAD:" + path, workdir: "PRIMARY"}));
}
text(await tools.exec_command({cmd: "bash helper.sh", workdir: workdir}));
