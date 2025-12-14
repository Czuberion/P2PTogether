import subprocess
import time
import re
import sys
import threading
import os
import signal

def run_streaming_interop_test():
    print("=== P2PTogether Video Streaming Verification ===")

    # 1. Start peer_service (Headless)
    print("[1] Starting peer_service (headless)...")
    env = os.environ.copy()
    env["GLOG_logtostderr"] = "1"
    
    # Ensure binaries exist
    if not os.path.exists("./peer_service"):
        print("ERROR: ./peer_service not found. Please build it first.")
        return False
        
    cmd_service = ["./peer_service", "-grpc-port", "0"]
    proc_service = subprocess.Popen(cmd_service, cwd="./", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1, env=env)

    service_peer_id = None
    service_p2p_port = None
    service_grpc_port = None
    
    # Regex to capture just the P2P ID and Port.
    regex_addr_strict = re.compile(r"Listening on address: /ip4/127\.0\.0\.1/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    regex_addr_loose = re.compile(r"Listening on address: .*/tcp/(\d+)/p2p/([a-zA-Z0-9]+)")
    regex_grpc = re.compile(r"gRPC server listening on 127.0.0.1:(\d+)")

    def read_service():
        nonlocal service_peer_id, service_p2p_port, service_grpc_port
        for line in iter(proc_service.stdout.readline, ''):
            # print(f"[Service] {line.strip()}") # Optional: debug log
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
    cmd_driver_create = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "create-session", "-session-name", "streamsess", "-username", "AdminStreamer"]
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

    # 3. Connect net_scenario to peer_service as Viewer
    print("[3] Connecting net_scenario (Viewer) to peer_service...")
    multiaddr = f"/ip4/127.0.0.1/tcp/{service_p2p_port}/p2p/{service_peer_id}"
    cmd_tester = ["./net_scenario", "-listen", "/ip4/127.0.0.1/tcp/0", "-connect", multiaddr, "-session", session_id, "-nickname", "ViewerPeer", "-role", "Viewer"]
    
    proc_tester = subprocess.Popen(cmd_tester, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

    patterns = {
        "auth_success": False,
        "video_received": False,
        "received_seqs": set()
    }

    # Regex to capture sequence number: [VIDEO] Received segment seq=(\d+)
    regex_seq = re.compile(r"\[VIDEO\] Received segment seq=(\d+)")

    def read_tester():
        for line in iter(proc_tester.stdout.readline, ''):
            sys.stdout.write(f"[Tester]  {line}")
            
            if "Auth Handshake SUCCESS" in line:
                patterns["auth_success"] = True
            
            # Check for Video segments
            # Expected log: [VIDEO] Received segment seq=...
            if "[VIDEO]" in line and "Received segment" in line:
                patterns["video_received"] = True
                m = regex_seq.search(line)
                if m:
                    seq = int(m.group(1))
                    patterns["received_seqs"].add(seq)

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

    # 4. Admin starts streaming via queue-add
    # The video file is assumed to be at 'tests/data/test_vid0.mp4' relative to project root?
    # User said: @[tests/data/test_vid0.mp4]
    # We are running from peer_service/. The file is likely at ../tests/data/test_vid0.mp4 relative to peer_service/
    # OR /home/mateusz/build/P2PTogether/tests/data/test_vid0.mp4
    
    video_path = "/home/mateusz/build/P2PTogether/tests/data/test_vid0.mp4"
    if not os.path.exists(video_path):
        print(f"WARNING: Video file not found at {video_path}. Testing might fail if ffmpeg fails.")
    
    print(f"[4] Admin starting stream with {video_path}...")
    cmd_driver_stream = ["./grpc_driver", "-addr", f"127.0.0.1:{service_grpc_port}", "-cmd", "queue-add", "-file", video_path]
    res_stream = subprocess.run(cmd_driver_stream, capture_output=True, text=True)
    print(res_stream.stdout)
    if res_stream.returncode != 0:
        print(f"ERROR: Stream start failed: {res_stream.stderr}")
    
    # Wait for video segments to arrive
    print("[5] Waiting for video segments (expecting sequences 0..5)...")
    wait_stream = time.time()
    
    # We want to see at least up to seq 5 (video is ~10s, 2s segments -> 0,1,2,3,4,5)
    desired_seq = 5
    expected_set = set(range(desired_seq + 1)) # {0,1,2,3,4,5}
    
    while time.time() - wait_stream < 30:
        if patterns["received_seqs"].issuperset(expected_set):
            break
        time.sleep(1)

    proc_service.terminate()
    proc_tester.terminate()
    t_svc.join()
    t_test.join()

    print("\n\n=== RESULTS ===")
    print(f"Auth Success: {patterns['auth_success']}")
    print(f"Received Seqs: {sorted(list(patterns['received_seqs']))}")
    
    if patterns["auth_success"] and patterns["received_seqs"].issuperset(expected_set):
        print("SUCCESS! Streaming Verified (Complete).")
        return True
    else:
        print(f"FAILURE. Missing segments: {expected_set - patterns['received_seqs']}")
        return False

if __name__ == "__main__":
    if run_streaming_interop_test():
        sys.exit(0)
    else:
        sys.exit(1)
