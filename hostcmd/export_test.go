package hostcmd

// allEntries lists every Entry variable of the table, for the test that
// every line is a shipped text.
func allEntries() []Entry {
	return []Entry{read, content, unknown, byPath, script, root, outside, allEnv, scriptFile, dockerOther, remove, move,
		create, perms, kill, archive, truncate, network, goClean, cargoClean, imagePrune, rmi, buildPrune, containerRm,
		dockerStop, volumeRm, systemPrune, serviceStop, vacuum}
}
