import subprocess
import time
import re
import sys
import threading
import os
import signal

def run_integration_test():
    print("=== P2PTogether Interop Verification ===")

    # 1. Start peer_service (Headless)
    print("[1] Starting peer_service (headless)...")
    # Using specific ports to avoid collisions and make connection predictable
    env = os.environ.copy()
    env["GLOG_logtostderr"] = "1"
    
    # cmd_service = ["go", "run", ".", "-grpc-port", "50051"]
    cmd_service = ["./peer_service", "-grpc-port", "0"]
    proc_service = subprocess.Popen(cmd_service, cwd="./", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1, env=env)

    service_peer_id = None
    service_p2p_port = None
    service_grpc_port = None
    service_logs = []
    
    # Regex to capture just the P2P ID and Port.
    # We look for "Listening on address: <addr>/p2p/<ID>"
    # We'll try to match IPv4 loopback first, but fallback if needed.
    regex_addr_strict = re.compile(r"Listening on address: /ip4/127\.0\.0\.1/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    regex_addr_loose = re.compile(r"Listening on address: .*/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    
    regex_grpc = re.compile(r"gRPC server listening on 127.0.0.1:(\d+)")

    def read_service():
        nonlocal service_peer_id, service_p2p_port, service_grpc_port, service_logs
        for line in iter(proc_service.stdout.readline, ''):
            service_logs.append(line)
            # Check for P2P Address
            if not service_peer_id:
                # Try strict first
                m = regex_addr_strict.search(line)
                if m:
                    service_p2p_port = m.group(1)
                    service_peer_id = m.group(2)
                    print(f"\n[Service] Captured ID: {service_peer_id} on Port: {service_p2p_port} (Strict)\n")
                else:
                    # Try loose if strict failed
                    m2 = regex_addr_loose.search(line)
                    if m2:
                         service_p2p_port = m2.group(1)
                         service_peer_id = m2.group(2)
                         print(f"\n[Service] Captured ID: {service_peer_id} on Port: {service_p2p_port} (Loose)\n")

            if not service_grpc_port:
                m = regex_grpc.search(line)
                if m:
                    service_grpc_port = m.group(1)
                    print(f"\n[Service] Captured gRPC Port: {service_grpc_port}\n")

    t_svc = threading.Thread(target=read_service)
    t_svc.start()

    # Wait for service to obtain ID and gRPC port (Polling up to 15s)
    start_wait = time.time()
    while time.time() - start_wait < 15:
        if service_peer_id and service_grpc_port:
            break
        time.sleep(0.5)

    if not service_peer_id or not service_grpc_port:
        print("ERROR: peer_service failed to start or didn't log address/port in time.")
        print("--- SERVICE OUTPUT START ---")
        for l in service_logs:
            sys.stdout.write(l)
        print("--- SERVICE OUTPUT END ---")
        proc_service.terminate()
        return False

    # 2. Use grpc_driver to create a session
    print(f"[2] Creating session via grpc_driver on port {service_grpc_port}...")
    cmd_driver = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "create-session", "-session-name", "interopsess", "-username", "AdminUser"]
    result_driver = subprocess.run(cmd_driver, capture_output=True, text=True)
    
    if result_driver.returncode != 0:
        print(f"ERROR: grpc_driver failed: {result_driver.stderr}")
        proc_service.terminate()
        return False
        
    print(f"[Driver Output]\n{result_driver.stdout}")
    
    # Extract Session ID from driver output
    session_id = None
    for line in result_driver.stdout.splitlines():
        if line.startswith("SESSION_ID:"):
            session_id = line.split(":")[1].strip()
            break
            
    if not session_id:
        print("ERROR: Could not get Session ID from driver output.")
        proc_service.terminate()
        return False
        
    print(f"Created Session: {session_id}")

    # 3. Connect net_scenario to peer_service
    print("[3] Connecting net_scenario to peer_service...")
    multiaddr = f"/ip4/127.0.0.1/tcp/{service_p2p_port}/p2p/{service_peer_id}"
    cmd_tester = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/0", "-connect", multiaddr, "-session", session_id, "-nickname", "TesterParams", "-role", "Viewer"]
    
    proc_tester = subprocess.Popen(cmd_tester, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

    patterns = {"auth_success": False, "chat_echo_received": False} # We check if we can hear chat
    # Note: If peer_service echoes chat back to sender (or distributes it), we might see other chat or our own?
    # net_scenario listens to chat topic.
    
    def read_tester():
        for line in iter(proc_tester.stdout.readline, ''):
            sys.stdout.write(f"[Tester]  {line}")
            if "Auth Handshake SUCCESS" in line:
                patterns["auth_success"] = True
            if "[CHAT]" in line and "Hello Service" in line:
                 # If net_scenario prints its own sent messages upon subscribing? 
                 # Or if peer_service re-broadcasts?
                 # Actually, GossipSub echoes messages unless disabled.
                 patterns["chat_echo_received"] = True

    t_test = threading.Thread(target=read_tester)
    t_test.start()

    time.sleep(3)
    
    if not patterns["auth_success"]:
        print("ERROR: Auth Handshake failed.")
        print("--- SERVICE OUTPUT START ---")
        for l in service_logs:
            sys.stdout.write(l)
        print("--- SERVICE OUTPUT END ---")
        proc_service.terminate()
        proc_tester.terminate()
        return False
        
    print("[Tester] Sending chat message...")
    proc_tester.stdin.write("chat Hello Service\n")
    proc_tester.stdin.flush()
    
    time.sleep(3)
    
    proc_service.terminate()
    proc_tester.terminate()
    t_svc.join()
    t_test.join()

    # Success criteria: 
    # 1. Auth success (means we talked to real service via /auth/1)
    # 2. (Optional) Chat echo.
    
    if patterns["auth_success"]:
        print("\n\nSUCCESS! Interoperability verified.")
        return True
    else:
        print("\n\nFAILURE! Auth handshake missed.")
        return False

if __name__ == "__main__":
    if run_integration_test():
        sys.exit(0)
    else:
        sys.exit(1)
