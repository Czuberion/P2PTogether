import subprocess
import time
import re
import sys
import threading
import os
import signal

def run_complex_interop_test():
    print("=== P2PTogether Complex Interop Verification ===")

    # 1. Start peer_service (Headless)
    print("[1] Starting peer_service (headless)...")
    env = os.environ.copy()
    env["GLOG_logtostderr"] = "1"
    
    cmd_service = ["./peer_service", "-grpc-port", "0"]
    proc_service = subprocess.Popen(cmd_service, cwd="./", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1, env=env)

    service_peer_id = None
    service_p2p_port = None
    service_grpc_port = None
    service_logs = []
    
    # Regex to capture just the P2P ID and Port.
    regex_addr_strict = re.compile(r"Listening on address: /ip4/127\.0\.0\.1/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    regex_addr_loose = re.compile(r"Listening on address: .*/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    
    regex_grpc = re.compile(r"gRPC server listening on 127.0.0.1:(\d+)")

    def read_service():
        nonlocal service_peer_id, service_p2p_port, service_grpc_port, service_logs
        for line in iter(proc_service.stdout.readline, ''):
            service_logs.append(line)
            if not service_peer_id:
                m = regex_addr_strict.search(line)
                if m:
                    service_p2p_port = m.group(1)
                    service_peer_id = m.group(2)
                    print(f"\n[Service] Captured ID: {service_peer_id} on Port: {service_p2p_port} (Strict)\n")
                else:
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
        proc_service.terminate()
        return False

    # 2. Use grpc_driver to create a session
    print(f"[2] Creating session via grpc_driver on port {service_grpc_port}...")
    cmd_driver_create = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "create-session", "-session-name", "complexsess", "-username", "AdminUser"]
    result_create = subprocess.run(cmd_driver_create, capture_output=True, text=True)
    
    if result_create.returncode != 0:
        print(f"ERROR: grpc_driver create-session failed: {result_create.stderr}")
        proc_service.terminate()
        return False
        
    print(f"[Driver Create] OK.\n{result_create.stdout}")
    # Extract Session ID
    session_id = None
    for line in result_create.stdout.splitlines():
        if line.startswith("SESSION_ID:"):
            session_id = line.split(":")[1].strip()
            break
            
    if not session_id:
        print("ERROR: Could not get Session ID.")
        proc_service.terminate()
        return False
        
    print(f"Created Session: {session_id}")

    # 3. Connect net_scenario to peer_service
    print("[3] Connecting net_scenario to peer_service...")
    multiaddr = f"/ip4/127.0.0.1/tcp/{service_p2p_port}/p2p/{service_peer_id}"
    cmd_tester = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/0", "-connect", multiaddr, "-session", session_id, "-nickname", "ViewerPeer", "-role", "Viewer"]
    
    proc_tester = subprocess.Popen(cmd_tester, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

    tester_peer_id = None
    patterns = {
        "auth_success": False, 
        "admin_chat_received": False, 
        "role_update_received": False
    }

    regex_tester_id = re.compile(r"I am ([a-zA-Z0-9]+) aka 'ViewerPeer'")

    def read_tester():
        nonlocal tester_peer_id
        for line in iter(proc_tester.stdout.readline, ''):
            sys.stdout.write(f"[Tester]  {line}")
            
            if not tester_peer_id:
                m = regex_tester_id.search(line)
                if m:
                    tester_peer_id = m.group(1)
                    print(f"\n[Tester] Captured Peer ID: {tester_peer_id}\n")

            if "Auth Handshake SUCCESS" in line:
                patterns["auth_success"] = True
            
            # Check for Admin's chat
            if "[CHAT]" in line and "Hello from Admin" in line:
                patterns["admin_chat_received"] = True
            
            # Check for Role Assignment
            # Log format: [CTRL] Role Assignment: Peer <ID> -> Roles [streamer]
            if "[CTRL] Role Assignment:" in line and "streamer" in line:
                 if tester_peer_id and tester_peer_id in line:
                     patterns["role_update_received"] = True

    t_test = threading.Thread(target=read_tester)
    t_test.start()

    time.sleep(5)
    if not patterns["auth_success"]:
        print("ERROR: Auth Handshake failed or too slow.")
        proc_service.terminate()
        proc_tester.terminate()
        return False
        
    print("Auth success. Wait for DHT/Topic stable...")
    time.sleep(5)

    if not tester_peer_id:
        print("ERROR: Could not capture Tester Peer ID.")
        proc_service.terminate()
        proc_tester.terminate()
        return False

    # 4. Admin sends chat
    print("[4] Admin sending chat message...")
    cmd_driver_chat = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "send-chat", "-message", "Hello from Admin"]
    subprocess.run(cmd_driver_chat, capture_output=True) # Check result?
    
    time.sleep(2)
    
    # 5. Viewer sends chat back (Optional, just to keep activity)
    print("[5] Viewer sending chat message...")
    proc_tester.stdin.write("chat Hello back\n")
    proc_tester.stdin.flush()
    
    time.sleep(2)

    # 6. Admin assigns Role 'Streamer' to Viewer
    print(f"[6] Admin assigning 'Streamer' role to {tester_peer_id}...")
    cmd_driver_role = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "assign-role", "-target-peer", tester_peer_id, "-roles", "Streamer"]
    subprocess.run(cmd_driver_role, capture_output=True)

    time.sleep(5) # Wait for propagation

    proc_service.terminate()
    proc_tester.terminate()
    t_svc.join()
    t_test.join()

    print("\n\n=== RESULTS ===")
    print(f"Auth Success: {patterns['auth_success']}")
    print(f"Admin Chat Received: {patterns['admin_chat_received']}")
    print(f"Role Update Received: {patterns['role_update_received']}")
    
    if patterns["auth_success"] and patterns["admin_chat_received"] and patterns["role_update_received"]:
        print("SUCCESS! Complex Scenario Verified.")
        return True
    else:
        print("FAILURE.")
        return False

if __name__ == "__main__":
    if run_complex_interop_test():
        sys.exit(0)
    else:
        sys.exit(1)
