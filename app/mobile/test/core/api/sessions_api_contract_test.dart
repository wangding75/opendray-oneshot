import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:opendray/core/api/sessions_api.dart';

class _RecordingAdapter implements HttpClientAdapter {
  RequestOptions? lastRequest;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    lastRequest = options;
    return ResponseBody.fromString(
      '{}',
      200,
      headers: {
        Headers.contentTypeHeader: ['application/json'],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

void main() {
  late Dio dio;
  late _RecordingAdapter adapter;
  late SessionsApi api;

  setUp(() {
    dio = Dio(BaseOptions(baseUrl: 'https://gateway.example'));
    adapter = _RecordingAdapter();
    dio.httpClientAdapter = adapter;
    api = SessionsApi(dio);
  });

  test('input keeps the existing PTY endpoint and raw data payload', () async {
    await api.input('ses-123', 'echo hello\r');

    final request = adapter.lastRequest;
    expect(request, isNotNull);
    expect(request!.method, 'POST');
    expect(request.path, '/api/v1/sessions/ses-123/input');
    expect(request.data, {'data': 'echo hello\r'});
  });

  test('resize keeps the existing PTY endpoint and dimensions', () async {
    await api.resize('ses-123', cols: 132, rows: 43);

    final request = adapter.lastRequest;
    expect(request, isNotNull);
    expect(request!.method, 'POST');
    expect(request.path, '/api/v1/sessions/ses-123/resize');
    expect(request.data, {'cols': 132, 'rows': 43});
  });
}
