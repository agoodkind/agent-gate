const workdir = "WORKTREE";
for (const path of ["source.rb"]) {
  text(await tools.exec_command({cmd: "git show HEAD:" + path, workdir: "PRIMARY"}));
}
text(await tools.exec_command({cmd: "ruby helper.rb", workdir: workdir}));
