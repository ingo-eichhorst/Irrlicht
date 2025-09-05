#!/usr/bin/env python3
"""
stress-test.py - Stress testing and error injection tool for Irrlicht hook receiver

This tool performs comprehensive testing of the production hook receiver including:
- Performance stress testing with high event rates
- Error injection and recovery testing
- Resource usage monitoring
- Concurrent session simulation
- Log rotation verification
"""

import json
import subprocess
import time
import threading
import random
import os
import sys
import tempfile
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor
import signal

class StressTestRunner:
    def __init__(self, hook_binary_path='./tools/irrlicht-hook/irrlicht-hook'):
        self.hook_binary = hook_binary_path
        self.results = {
            'events_processed': 0,
            'errors': 0,
            'timeouts': 0,
            'latencies': [],
            'start_time': None,
            'end_time': None
        }
        self.running = True
        
    def generate_event(self, session_id=None, event_type=None):
        """Generate a realistic hook event"""
        if not session_id:
            session_id = f"sess_stress_{random.randint(100000, 999999)}"
        
        if not event_type:
            event_type = random.choice([
                'SessionStart', 'UserPromptSubmit', 'Notification', 
                'Stop', 'SubagentStop', 'SessionEnd'
            ])
        
        import os
        username = os.environ.get('USER', 'testuser')
        event = {
            'hook_event_name': event_type,
            'session_id': session_id,
            'timestamp': f"{int(time.time() * 1000000)}",
            'data': {
                'transcript_path': f'/Users/{username}/.claude/projects/stress-test-{session_id}/transcript.jsonl',
                'cwd': f'/Users/{username}/projects/stress-test-{session_id}',
                'model': random.choice(['claude-3.7-sonnet', 'claude-3-haiku', 'claude-3-opus']),
            }
        }
        
        # Add event-specific data
        if event_type == 'UserPromptSubmit':
            event['data'].update({
                'prompt_length': random.randint(10, 5000),
                'has_attachments': random.choice([True, False]),
                'message_id': f'msg_{random.randint(1000, 9999)}'
            })
        elif event_type == 'Notification':
            event['data'].update({
                'notification_type': random.choice(['user_input_required', 'confirmation_required']),
                'message': 'Test notification message',
                'context': 'stress_test'
            })
        
        return event
    
    def generate_malformed_event(self):
        """Generate malformed events for error testing"""
        malformed_types = [
            '{"invalid": "json"',  # Truncated JSON
            '{"hook_event_name": "InvalidEvent"}',  # Invalid event type
            '{"session_id": ""}',  # Empty session ID
            json.dumps({'hook_event_name': 'SessionStart', 'session_id': 'x' * 600000}),  # Oversized
            '{}',  # Missing required fields
        ]
        return random.choice(malformed_types)
    
    def send_event(self, event_data, timeout=5):
        """Send a single event to the hook receiver"""
        start_time = time.time()
        
        try:
            if isinstance(event_data, dict):
                json_data = json.dumps(event_data)
            else:
                json_data = event_data
                
            process = subprocess.run(
                [self.hook_binary],
                input=json_data,
                text=True,
                capture_output=True,
                timeout=timeout
            )
            
            latency = (time.time() - start_time) * 1000  # Convert to milliseconds
            
            if process.returncode == 0:
                self.results['events_processed'] += 1
                self.results['latencies'].append(latency)
                return True, latency, process.stdout
            else:
                self.results['errors'] += 1
                return False, latency, process.stderr
                
        except subprocess.TimeoutExpired:
            self.results['timeouts'] += 1
            return False, timeout * 1000, 'Process timed out'
        except Exception as e:
            self.results['errors'] += 1
            return False, 0, str(e)
    
    def performance_stress_test(self, duration_seconds=60, events_per_second=5):
        """Run performance stress test"""
        print(f"Starting performance stress test:")
        print(f"  Duration: {duration_seconds}s")
        print(f"  Target rate: {events_per_second} events/second")
        print(f"  Expected total: {duration_seconds * events_per_second} events")
        
        self.results['start_time'] = time.time()
        end_time = self.results['start_time'] + duration_seconds
        
        session_pool = [f"sess_perf_{i}" for i in range(8)]  # 8 concurrent sessions
        
        def event_sender():
            while time.time() < end_time and self.running:
                session_id = random.choice(session_pool)
                event = self.generate_event(session_id)
                success, latency, output = self.send_event(event)
                
                if not success and 'validation failed' not in output:
                    print(f"⚠️  Event failed: {output[:100]}")
                
                # Rate limiting
                time.sleep(1.0 / events_per_second)
        
        # Run with multiple threads for higher throughput
        with ThreadPoolExecutor(max_workers=3) as executor:
            futures = [executor.submit(event_sender) for _ in range(3)]
            
            # Monitor progress
            while time.time() < end_time and self.running:
                time.sleep(5)
                elapsed = time.time() - self.results['start_time']
                rate = self.results['events_processed'] / elapsed if elapsed > 0 else 0
                print(f"  Progress: {elapsed:.1f}s, {self.results['events_processed']} events, {rate:.1f} events/s")
            
            # Stop all threads
            self.running = False
            for future in futures:
                future.cancel()
        
        self.results['end_time'] = time.time()
        print(f"✅ Performance stress test completed")
    
    def error_injection_test(self):
        """Test error handling and recovery"""
        print(f"Starting error injection test...")
        
        error_scenarios = [
            ('Malformed JSON', self.generate_malformed_event),
            ('Oversized payload', lambda: json.dumps({'x': 'y' * 700000})),
            ('Empty input', lambda: ''),
            ('Invalid event type', lambda: json.dumps({
                'hook_event_name': 'InvalidEvent',
                'session_id': 'sess_error_test',
                'timestamp': str(int(time.time()))
            })),
            ('Missing session_id', lambda: json.dumps({
                'hook_event_name': 'SessionStart',
                'timestamp': str(int(time.time()))
            })),
        ]
        
        for test_name, generator in error_scenarios:
            print(f"  Testing: {test_name}")
            malformed_data = generator()
            success, latency, output = self.send_event(malformed_data)
            
            # These should all fail gracefully
            if success:
                print(f"    ⚠️  Expected failure but got success")
            else:
                print(f"    ✅ Failed as expected: {output[:50]}")
        
        print(f"✅ Error injection test completed")
    
    def concurrent_sessions_test(self, num_sessions=8, events_per_session=10):
        """Test handling of multiple concurrent sessions"""
        print(f"Starting concurrent sessions test:")
        print(f"  Sessions: {num_sessions}")
        print(f"  Events per session: {events_per_session}")
        
        def session_worker(session_id):
            events = ['SessionStart', 'UserPromptSubmit', 'UserPromptSubmit', 
                     'Notification', 'UserPromptSubmit', 'Stop']
            
            for i in range(events_per_session):
                event_type = events[i % len(events)]
                event = self.generate_event(session_id, event_type)
                success, latency, output = self.send_event(event)
                
                if not success:
                    print(f"    Session {session_id} event {i} failed: {output[:50]}")
                
                # Small delay between events in same session
                time.sleep(0.1)
        
        # Run all sessions concurrently
        with ThreadPoolExecutor(max_workers=num_sessions) as executor:
            session_ids = [f"sess_concurrent_{i:02d}" for i in range(num_sessions)]
            futures = [executor.submit(session_worker, sid) for sid in session_ids]
            
            # Wait for all to complete
            for future in futures:
                future.result()
        
        print(f"✅ Concurrent sessions test completed")
    
    def log_rotation_test(self):
        """Test log rotation functionality"""
        print(f"Starting log rotation test...")
        
        # Generate enough events to trigger log rotation (assuming 10MB limit)
        # Each log entry is ~200 bytes, so we need ~50k events
        large_event = self.generate_event()
        large_event['data']['large_field'] = 'x' * 1000  # Make events larger
        
        print(f"  Generating large volume of events to trigger rotation...")
        for i in range(100):  # Start with smaller number for testing
            success, latency, output = self.send_event(large_event)
            if i % 10 == 0:
                print(f"    Sent {i} events...")
        
        # Check if log files exist
        logs_dir = Path.home() / "Library/Application Support/Irrlicht/logs"
        if logs_dir.exists():
            log_files = list(logs_dir.glob("events.log*"))
            print(f"  Found {len(log_files)} log files: {[f.name for f in log_files]}")
        
        print(f"✅ Log rotation test completed")
    
    def resource_monitoring_test(self, duration=30):
        """Monitor resource usage during sustained load"""
        print(f"Starting resource monitoring test for {duration}s...")
        
        # This is a simplified version - in production you'd use psutil
        session_pool = [f"sess_monitor_{i}" for i in range(4)]
        end_time = time.time() + duration
        
        while time.time() < end_time and self.running:
            session_id = random.choice(session_pool)
            event = self.generate_event(session_id)
            success, latency, output = self.send_event(event)
            
            # Log high latencies
            if latency > 200:  # >200ms is considered slow
                print(f"    ⚠️  High latency: {latency:.1f}ms")
            
            time.sleep(0.1)  # 10 events/second
        
        print(f"✅ Resource monitoring test completed")
    
    def print_results(self):
        """Print comprehensive test results"""
        if not self.results['latencies']:
            print("No successful events processed")
            return
        
        duration = self.results['end_time'] - self.results['start_time'] if self.results['end_time'] else 0
        avg_latency = sum(self.results['latencies']) / len(self.results['latencies'])
        p95_latency = sorted(self.results['latencies'])[int(len(self.results['latencies']) * 0.95)]
        
        print(f"\n📊 Test Results Summary:")
        print(f"  Duration: {duration:.1f}s")
        print(f"  Events processed: {self.results['events_processed']}")
        print(f"  Errors: {self.results['errors']}")
        print(f"  Timeouts: {self.results['timeouts']}")
        print(f"  Average latency: {avg_latency:.1f}ms")
        print(f"  P95 latency: {p95_latency:.1f}ms")
        
        if duration > 0:
            throughput = self.results['events_processed'] / duration
            print(f"  Throughput: {throughput:.1f} events/second")
        
        # Performance assessment
        print(f"\n📈 Performance Assessment:")
        if avg_latency < 50:
            print(f"  ✅ Excellent average latency ({avg_latency:.1f}ms < 50ms)")
        elif avg_latency < 200:
            print(f"  ✅ Good average latency ({avg_latency:.1f}ms < 200ms)")
        else:
            print(f"  ⚠️  High average latency ({avg_latency:.1f}ms >= 200ms)")
        
        if p95_latency < 200:
            print(f"  ✅ Good P95 latency ({p95_latency:.1f}ms < 200ms)")
        else:
            print(f"  ⚠️  High P95 latency ({p95_latency:.1f}ms >= 200ms)")
    
    def run_full_test_suite(self):
        """Run the complete stress test suite"""
        print(f"🧪 Starting Irrlicht Hook Receiver Stress Test Suite")
        print(f"Hook binary: {self.hook_binary}")
        print(f"=" * 60)
        
        # Set up signal handler for graceful shutdown
        def signal_handler(sig, frame):
            print(f"\n⚠️  Received interrupt signal, stopping tests...")
            self.running = False
        
        signal.signal(signal.SIGINT, signal_handler)
        
        try:
            # Test 1: Error injection (should be safe and fast)
            self.error_injection_test()
            print()
            
            # Test 2: Concurrent sessions
            self.concurrent_sessions_test()
            print()
            
            # Test 3: Performance stress test
            self.performance_stress_test(duration_seconds=30, events_per_second=10)
            print()
            
            # Test 4: Resource monitoring
            self.resource_monitoring_test(duration=20)
            print()
            
            # Test 5: Log rotation (may generate many files)
            self.log_rotation_test()
            print()
            
        except KeyboardInterrupt:
            print(f"\n⚠️  Tests interrupted by user")
        
        finally:
            self.print_results()

def main():
    import argparse
    parser = argparse.ArgumentParser(description='Stress test the Irrlicht hook receiver')
    parser.add_argument('--hook-binary', default='./tools/irrlicht-hook/irrlicht-hook',
                      help='Path to irrlicht-hook binary')
    parser.add_argument('--test', choices=['performance', 'errors', 'concurrent', 'rotation', 'monitoring', 'all'],
                      default='all', help='Which test to run')
    parser.add_argument('--duration', type=int, default=30, help='Duration for performance tests (seconds)')
    parser.add_argument('--rate', type=int, default=10, help='Events per second for performance test')
    
    args = parser.parse_args()
    
    # Check if hook binary exists
    if not os.path.exists(args.hook_binary):
        print(f"❌ Hook binary not found: {args.hook_binary}")
        print(f"Build it with: cd tools/irrlicht-hook && go build -o irrlicht-hook .")
        sys.exit(1)
    
    runner = StressTestRunner(args.hook_binary)
    
    if args.test == 'all':
        runner.run_full_test_suite()
    elif args.test == 'performance':
        runner.performance_stress_test(args.duration, args.rate)
        runner.print_results()
    elif args.test == 'errors':
        runner.error_injection_test()
    elif args.test == 'concurrent':
        runner.concurrent_sessions_test()
    elif args.test == 'rotation':
        runner.log_rotation_test()
    elif args.test == 'monitoring':
        runner.resource_monitoring_test(args.duration)
        runner.print_results()

if __name__ == '__main__':
    main()