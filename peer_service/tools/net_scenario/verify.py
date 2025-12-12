import subprocess
import time
import re
import sys
import threading

def read_output(proc, name, patterns_found):
    for line in iter(proc.stdout.readline, ''):
        print(f"[{name}] {line}", end='')
        for p_name, p_regex in patterns_found.items():
            if p_regex.search(line):
                # print(f"MATCH: {p_name} in {name}")
                patterns_found[p_name] = True
    proc.stdout.close()

def run_verification():
    print("Starting Instance A (Admin)...")
    cmd_a = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/0", "-create", "-role", "Admin", "-session", "verifysess", "-nickname", "Alice"]
    proc_a = subprocess.Popen(cmd_a, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

    # Patterns to look for
    patterns_a = {"id": False, "chat_received": False}
    regex_id = re.compile(r"I am ([a-zA-Z0-9]+) aka")
    # regex_chat = re.compile(r"\[CHAT\] Bob: Hello Alice") # Old
    regex_chat = re.compile(r"\[CHAT\] ([a-zA-Z0-9]+): Hello Alice") # New: Matches PeerID
    
    peer_id_a = None

    # Thread to read A
    def read_a():
        nonlocal peer_id_a
        for line in iter(proc_a.stdout.readline, ''):
            sys.stdout.write(f"[Alice] {line}")
            if not peer_id_a:
                m = regex_id.search(line)
                if m:
                    peer_id_a = m.group(1)
                    print(f"\nCaptured Alice ID: {peer_id_a}\n")
            if regex_chat.search(line):
                patterns_a["chat_received"] = True

    t_a = threading.Thread(target=read_a)
    t_a.start()

    # Wait for ID
    time.sleep(2)
    if not peer_id_a:
        print("Failed to get Alice ID")
        proc_a.terminate()
        return False

    print("Starting Instance B (Viewer)...")
    connect_addr = f"/ip4/127.0.0.1/tcp/0/p2p/{peer_id_a}" # Wait, port is random, we need proper multiaddr
    # My main.go prints "Listening on: [...]"
    # I need to capture the full multiaddr or just the port.
    # Actually, main.go prints `h.Addrs()` which is a list.
    # But `h.Addrs()` usually returns e.g. `/ip4/127.0.0.1/tcp/34567`
    # We need to parse that too.
    
    # Let's restart logic to capture addresses.
    # I'll just hardcode port 9011 for A and 9012 for B to simplify.
    proc_a.terminate()
    t_a.join()
    
    print("Restarting with fixed ports...")
    cmd_a = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/9011", "-create", "-role", "Admin", "-session", "verifysess", "-nickname", "Alice"]
    proc_a = subprocess.Popen(cmd_a, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
    
    peer_id_a = None
    patterns_a["chat_received"] = False
    
    t_a = threading.Thread(target=read_a)
    t_a.start()
    
    time.sleep(2)
    if not peer_id_a:
        print("Failed to get Alice ID (fixed port run)")
        proc_a.terminate()
        return False

    # Connect string
    connect_str = f"/ip4/127.0.0.1/tcp/9011/p2p/{peer_id_a}"
    cmd_b = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/9012", "-connect", connect_str, "-session", "verifysess", "-nickname", "Bob", "-role", "Viewer"]
    proc_b = subprocess.Popen(cmd_b, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

    def read_b():
        for line in iter(proc_b.stdout.readline, ''):
             sys.stdout.write(f"[Bob]   {line}")

    t_b = threading.Thread(target=read_b)
    t_b.start()

    time.sleep(3) # Wait for auth handshake

    print("Sending Chat from Bob...")
    proc_b.stdin.write("chat Hello Alice\n")
    proc_b.stdin.flush()

    time.sleep(2)

    print("Checking results...")
    proc_a.terminate()
    proc_b.terminate()
    t_a.join()
    t_b.join()

    if patterns_a["chat_received"]:
        print("SUCCESS: Alice received chat from Bob.")
        return True
    else:
        print("FAILURE: Alice did NOT receive chat.")
        return False

if __name__ == "__main__":
    success = run_verification()
    sys.exit(0 if success else 1)
